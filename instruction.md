# Recover interrupted OCI content-graph replication

## Background

This repository is ORAS Go, used by an artifact staging gateway to copy nested
OCI content-addressable graphs between registries and local stores. Several
workers can copy overlapping graphs at once. A previous copy can be interrupted
after blobs have arrived but before one or more child manifests are durably
committed. The destination may then report those child manifests as present
while rejecting their parent index. In production the retry repeats the same
failure, malformed descriptors can panic a worker, and cancellation can return
while scheduled work is still using shared concurrency permits.

Repair the implementation under `/app`. Do not replace the library with a
different implementation or change its public API. No external registry is
needed: all required behavior can be reproduced with the repository's in-memory
and filesystem stores.

## Required behavior

### 1. Interrupted graph copies must self-heal

`oras.CopyGraph` must recover a partially committed manifest graph when all of
the following are true:

- an interior manifest is reported as existing and is therefore skipped;
- pushing its parent manifest or index is rejected by the destination because
  referenced content is missing; and
- the rejection is represented by a typed registry error code
  `MANIFEST_BLOB_UNKNOWN`, `BLOB_UNKNOWN`, or `MANIFEST_UNKNOWN`.

On that failure, re-copy the current node's **immediate manifest successors**
from the source even if the destination's existence check says they are
present, then retry the failed parent push once. Do not re-copy blob successors
on this recovery path. A successfully recovered index, its child manifests,
and their content must be fetchable from the destination.

Recognize both a single typed registry error and a typed multi-error, including
either form wrapped by the library's normal copy errors. Do not classify an
error by matching its text. An unrelated registry code, a plain error whose
message merely contains one of the code names, or a non-registry error must be
returned without a recovery retry.

Recovery is bounded: retry the failed parent push at most once. If re-copying a
manifest successor fails, return that failure. If the retry is rejected again,
return the retry failure. Preserve the existing `PreCopy`, `PostCopy`,
`OnCopySkipped`, mounting, cache, `CopyError` origin, and context-cancellation
semantics on all ordinary paths.

### 2. Limited concurrent work must have one owner and one terminal cause

`internal/syncutil.Go` must obey the supplied semaphore limit and execute each
started item at most once. When any callback fails or the parent context is
cancelled:

- stop scheduling new callbacks;
- cancel callbacks that observe the shared context;
- wait for every callback that was already started to finish and release its
  exact `LimitedRegion` before returning;
- release every acquired semaphore permit on every exit path; and
- return the causal callback error, or the parent context's cancellation cause
  when cancellation happened first.

It must not return a later `context.Canceled` in place of an earlier callback
failure. Concurrent callbacks may return different concrete error types; this
must not panic or race. Calls with no limiter must keep their existing
unlimited behavior. `LimitedGroup` behavior and its public surface must remain
compatible.

### 3. Untrusted descriptors must fail as errors, never panics

Descriptor digests are untrusted input. Before using digest operations that
require a registered, syntactically valid algorithm, validate the digest.

- `content.NewVerifyReader`, `content.ReadAll`, and store `Push` operations
  must not panic for a malformed digest or an unsupported algorithm.
- Reads and pushes must return errors that retain the underlying digest
  validation error so callers can use `errors.Is`.
- File, memory, and OCI stores must not publish content after such a failed
  push.
- Remote referrer fallback must reject an invalid subject digest before
  issuing an HTTP request.
- Valid SHA-256 descriptors and the existing mismatch/trailing-data behavior
  must remain unchanged.

### 4. `content.ReadAll` must be both bounded and complete

Never allocate directly from an untrusted descriptor size. A negative size
must return `content.ErrInvalidDescriptorSize`. A forged enormous positive
size (for example `1<<62`) paired with a short stream must return a verification
error such as `io.ErrUnexpectedEOF` without panicking or attempting an
enormous allocation.

The allocation guard is not a content-size limit: valid digest-verified blobs
larger than 32 MiB must still be read completely and returned byte-for-byte.
Short content, trailing content, and digest mismatch must continue to return
their existing typed errors.

### 5. In-memory graph identity is the digest

`internal/graph.Memory` must identify a node solely by its digest. Descriptors
with the same digest but different media type, size, annotations, or other
metadata are aliases of one node, not distinct graph vertices.

`Index`, `IndexAll`, `Exists`, `Predecessors`, `Remove`, and `DigestSet` must use
that identity consistently. Aliases must not duplicate nodes or edges;
predecessor lookup through an alias must return the indexed predecessor; and
removing through an alias must remove the digest node and update dangling-edge
state exactly once. Graph operations must remain safe under concurrent reads.

### 6. Named file restoration must tolerate only benign races

When the file store restores named successors from fallback CAS content,
concurrent workers can materialize the same name between the existence check
and the restore. Treat the store's duplicate-name result as a benign race and
complete the manifest push successfully when the intended content is already
present. A missing successor remains permissible so manifests can arrive
before blobs.

Do not swallow other fetch, validation, path, or I/O failures. Those errors
must still propagate, and a successful operation must leave the named file
with the descriptor's verified content.

## Compatibility and completion

- Work only inside `/app`; keep the module path and all exported APIs stable.
- Preserve the Apache-2.0 license and existing normal copy, fetch, push, tag,
  callback, and error-wrapping behavior not changed by the requirements above.
- Do not add unbounded background processes or depend on a live registry.
- The repair must be race-free on the affected concurrent paths and must pass
  the repository's Go test suite.

# Executable requirements map

Every normative branch in `instruction.md` is tied to a named executable test
below. `tests/test.sh` installs these Go files into the real ORAS package paths,
runs the complete repository suite, and then repeats the concurrency-sensitive
packages under the race detector.

| Requirement | Direct executable coverage |
| --- | --- |
| Typed graph recovery for all three codes, single/multi/wrapped forms | `TestTalentsCopyGraphRecognizesEveryTypedMissingReferenceForm`, `TestTalentsCopyGraphSelfHealsTypedMissingReferences` |
| Immediate manifest-only replay, one parent retry, causal replay failure | `TestTalentsCopyGraphSelfHealsTypedMissingReferences`, `TestTalentsCopyGraphRecoveryRetriesParentOnlyOnce`, `TestTalentsCopyGraphReturnsRecoverySuccessorSourceFailure` |
| No text matching or unrelated-code recovery; ordinary callbacks | `TestTalentsCopyGraphDoesNotStringMatchOrRetryUnrelatedErrors`, `TestTalentsCopyGraphOrdinaryCallbacksRemainStable` |
| Scheduler ownership, join, first cause, parent cancellation, permits | `TestTalentsGoWaitsForStartedWorkersAndReturnsCause`, `TestTalentsGoCancellationWhileWaitingJoinsStartedWorker`, `TestTalentsGoPreservesParentCancellationCause` |
| Limited, unlimited and `LimitGroup` compatibility | `TestTalentsGoHonorsLimitAndRunsEachItemOnce`, `TestTalentsGoWithoutLimiterRunsEveryItemExactlyOnce`, `TestTalentsLimitGroupKeepsBoundAndFirstTaskError` |
| Heterogeneous concurrent failures | `TestTalentsGoDifferentConcreteErrorsDoNotPanic` plus the repeated race run |
| Malformed and unsupported digest safety in readers and three stores | `TestTalentsInvalidDigestIsReportedWithoutPanic`, `TestTalentsUnsupportedDigestIsReportedWithoutPanic`, `TestTalentsStoresRejectBadDigestsWithoutPublishing` |
| Referrer validation before HTTP | `TestTalentsReferrerFallbackValidatesDigestBeforeNetwork` |
| Bounded allocation, large valid content, short/trailing/mismatch errors | `TestTalentsReadAllBoundsForgedAllocationButAcceptsLargeContent`, `TestTalentsReadAllPreservesBoundaryErrors` |
| Digest-only graph identity through `Index`, `IndexAll`, aliases and removal | `TestTalentsMemoryUsesDigestIdentityAcrossMetadataAliases`, `TestTalentsMemoryIndexAllUsesDigestIdentityAcrossAliases` |
| Benign named-file race; fetch, digest, path, I/O and missing boundaries | all five `TestTalentsRestore...` tests in `file_restore_recovery_test.go` |
| Credential partial-read cleanup, unrelated-file preservation and 0600 retry | `TestTalentsCredentialIngestFailureIsAtomic` |
| OCI read/digest failure cleanup, no publication, unrelated file and 0444 retry | `TestTalentsOCIIngestFailureIsAtomic` |
| All three Pack paths reject media/digest before mutation and accept valid caller ownership | both `TestTalentsPack...` tests |
| Manifest length divergence/absence and media/digest/blob-length boundaries | `TestTalentsManifestFetchIgnoresTransportLengthDivergence`, `TestTalentsManifestFetchPreservesIntegrityBoundaries` |
| Catalog page cap, callback count, page-size query and zero-limit finite behavior | both `TestTalentsRepositoryCatalog...` tests |
| Digest-keyed proxy single-flight, causal failures, close, retry and cancellation | the first five `TestTalentsProxy...` tests |
| Cache non-NotFound, both AlreadyExists outcomes, dual-cause false races and uncached APIs | the final four `TestTalentsProxy...` tests, especially `TestTalentsProxyRejectsUnfetchableAlreadyExists` |
| Cross-origin redirect stripping, challenge containment and default-port identity | `TestTalentsAuthContainsCredentialsAcrossRedirectBoundaries`, `TestTalentsAuthRejectsCrossOriginChallengeBeforeCredentialLookup` |
| Same-origin compatibility and caller redirect callback preservation | `TestTalentsAuthPreservesSameOriginRedirectBehavior` |
| Unsafe bearer realm rejection before lookup/network and valid token-service controls | `TestTalentsAuthRejectsUnsafeBearerRealmsBeforeSideEffects`, `TestTalentsAuthAllowsCompatibleBearerRealms` |

The starter is never accepted by this suite. The reference patch must pass the
full suite and the repeated race run; plausible partial fixes are separately
checked as reward-zero negative controls before release.

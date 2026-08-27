/*
Copyright The ORAS Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package content

import (
	"bytes"
	_ "crypto/sha256"
	"errors"
	"io"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func talentsReadAllWithoutPanic(r io.Reader, desc ocispec.Descriptor) (data []byte, err error, panicValue any) {
	defer func() {
		panicValue = recover()
	}()
	data, err = ReadAll(r, desc)
	return
}

func TestTalentsInvalidDigestIsReportedWithoutPanic(t *testing.T) {
	body := []byte("descriptor payload")
	desc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Digest:    "invalid-digest",
		Size:      int64(len(body)),
	}
	got, err, panicValue := talentsReadAllWithoutPanic(bytes.NewReader(body), desc)
	if panicValue != nil {
		t.Fatalf("ReadAll panicked for invalid digest: %v", panicValue)
	}
	if got != nil {
		t.Fatalf("ReadAll returned data for invalid digest: %q", got)
	}
	if !errors.Is(err, digest.ErrDigestInvalidFormat) {
		t.Fatalf("ReadAll error = %v, want %v", err, digest.ErrDigestInvalidFormat)
	}

	var vr *VerifyReader
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("NewVerifyReader panicked: %v", recovered)
			}
		}()
		vr = NewVerifyReader(bytes.NewReader(body), desc)
	}()
	buf := make([]byte, len(body))
	if _, err := vr.Read(buf); !errors.Is(err, digest.ErrDigestInvalidFormat) {
		t.Fatalf("VerifyReader.Read error = %v, want digest validation error", err)
	}
	if err := vr.Verify(); !errors.Is(err, digest.ErrDigestInvalidFormat) {
		t.Fatalf("VerifyReader.Verify error = %v, want digest validation error", err)
	}
}

func TestTalentsReadAllBoundsForgedAllocationButAcceptsLargeContent(t *testing.T) {
	small := []byte("short actual body")
	forged := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Digest:    digest.FromBytes(small),
		Size:      1 << 62,
	}
	_, err, panicValue := talentsReadAllWithoutPanic(bytes.NewReader(small), forged)
	if panicValue != nil {
		t.Fatalf("ReadAll panicked for forged size: %v", panicValue)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ReadAll forged-size error = %v, want %v", err, io.ErrUnexpectedEOF)
	}

	const size = 33*1024*1024 + 257
	large := make([]byte, size)
	for i := range large {
		large[i] = byte((i*31 + 7) % 251)
	}
	desc := NewDescriptorFromBytes(ocispec.MediaTypeImageLayer, large)
	got, err, panicValue := talentsReadAllWithoutPanic(bytes.NewReader(large), desc)
	if panicValue != nil {
		t.Fatalf("ReadAll panicked for valid large content: %v", panicValue)
	}
	if err != nil {
		t.Fatal("ReadAll large-content error =", err)
	}
	if !bytes.Equal(got, large) {
		t.Fatal("ReadAll changed valid content larger than 32 MiB")
	}
}

func TestTalentsReadAllPreservesBoundaryErrors(t *testing.T) {
	body := []byte("abcdef")
	tests := []struct {
		name string
		desc ocispec.Descriptor
		want error
	}{
		{
			name: "negative size",
			desc: ocispec.Descriptor{MediaType: "test", Digest: digest.FromBytes(body), Size: -1},
			want: ErrInvalidDescriptorSize,
		},
		{
			name: "short stream",
			desc: ocispec.Descriptor{MediaType: "test", Digest: digest.FromBytes(body), Size: int64(len(body) + 1)},
			want: io.ErrUnexpectedEOF,
		},
		{
			name: "trailing stream",
			desc: ocispec.Descriptor{MediaType: "test", Digest: digest.FromBytes(body), Size: int64(len(body) - 1)},
			want: ErrTrailingData,
		},
		{
			name: "digest mismatch",
			desc: ocispec.Descriptor{MediaType: "test", Digest: digest.FromBytes([]byte("different")), Size: int64(len(body))},
			want: ErrMismatchedDigest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err, panicValue := talentsReadAllWithoutPanic(bytes.NewReader(body), tt.desc)
			if panicValue != nil {
				t.Fatalf("ReadAll panic = %v", panicValue)
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("ReadAll error = %v, want %v", err, tt.want)
			}
		})
	}
}

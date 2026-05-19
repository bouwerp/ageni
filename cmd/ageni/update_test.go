package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestDownloadAndExtractCopiesArchiveBeforeChecksumVerification(t *testing.T) {
	const (
		archiveName = "ageni-linux-amd64.tar.gz"
		binaryName  = "ageni-linux-amd64"
		binaryData  = "updated-binary"
	)

	archive := makeTarGzArchive(t, binaryName, []byte(binaryData))
	sum := sha256.Sum256(archive)
	checksum := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + archiveName:
			_, _ = w.Write(archive)
		case "/" + archiveName + ".sha256":
			_, _ = w.Write([]byte(checksum))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	gotPath, err := downloadAndExtract(server.URL+"/"+archiveName, server.URL+"/"+archiveName+".sha256", binaryName, ".tar.gz")
	if err != nil {
		t.Fatalf("downloadAndExtract() error = %v", err)
	}
	defer os.Remove(gotPath)

	got, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", gotPath, err)
	}
	if string(got) != binaryData {
		t.Fatalf("extracted binary = %q, want %q", string(got), binaryData)
	}
}

func makeTarGzArchive(t *testing.T, name string, data []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	hdr := &tar.Header{
		Name: name,
		Mode: 0o755,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar.Close() error = %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip.Close() error = %v", err)
	}

	return buf.Bytes()
}

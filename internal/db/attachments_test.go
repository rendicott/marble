package db

import (
	"strings"
	"testing"
)

func TestSniffAttachmentSVGAsDocument(t *testing.T) {
	svg := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
  <circle cx="12" cy="12" r="10"/>
</svg>`)
	mime, kind, err := SniffAttachment("icon.svg", svg)
	if err != nil {
		t.Fatalf("svg allowed as document: %v", err)
	}
	if mime != "image/svg+xml" || kind != "document" {
		t.Fatalf("got mime=%q kind=%q, want image/svg+xml document", mime, kind)
	}
}

func TestSniffAttachmentRejectsSVGWithoutTag(t *testing.T) {
	_, _, err := SniffAttachment("icon.svg", []byte("not really an svg"))
	if err == nil {
		t.Fatal("expected reject")
	}
}

func TestSniffAttachmentStillRejectsPDFAndAudio(t *testing.T) {
	if _, _, err := SniffAttachment("x.pdf", []byte("%PDF-1.4")); err == nil {
		t.Fatal("pdf should be rejected")
	}
	if _, _, err := SniffAttachment("x.mp3", []byte{0xFF, 0xFB, 0x90, 0x00}); err == nil {
		t.Fatal("mp3 should be rejected")
	}
}

func TestSniffAttachmentPNGUnchanged(t *testing.T) {
	// Minimal PNG header; DetectContentType recognizes PNG from 8-byte magic.
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 24))
	mime, kind, err := SniffAttachment("a.png", png)
	if err != nil {
		t.Fatalf("png: %v", err)
	}
	if mime != "image/png" || kind != "image" {
		t.Fatalf("got mime=%q kind=%q", mime, kind)
	}
}

package main

import (
	_ "embed"
	"net/http"
)

//go:embed assets/og-image.png
var ogImagePNG []byte

func ogImageHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(ogImagePNG)
}

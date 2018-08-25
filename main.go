package main

import (
	"archive/tar"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/dustin/go-humanize"
)

type ManifestItem struct {
	Config   string
	RepoTags []string
	Layers   []string
}

type Image struct {
	History []struct {
		EmptyLayer bool   `json:"empty_layer,omitempty"`
		CreatedBy  string `json:"created_by,omitempty"`
	} `json:"history,omitempty"`
}

type Layer struct {
	Files []*tar.Header
	Size  int64
}

const (
	humanizedWidth = 7
)

func run() error {
	tarPath := flag.String("f", "-", "layer.tar path")
	maxFiles := flag.Int("n", 10, "max files")
	lineWidth := flag.Int("l", 100, "screen line width")
	flag.Parse()

	var r io.Reader
	if *tarPath == "-" {
		r = os.Stdin
	} else {
		f, err := os.Open(*tarPath)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}

	var manifests []ManifestItem
	var img Image
	layers := make(map[string]*Layer)
	archive := tar.NewReader(r)
	for {
		hdr, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		switch {
		case strings.HasSuffix(hdr.Name, "/layer.tar"):
			record := tar.NewReader(archive)

			var fs []*tar.Header
			var total int64
			for {
				h, err := record.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
				fi := h.FileInfo()
				if fi.IsDir() {
					continue
				}
				fs = append(fs, h)
				total += h.Size
			}
			layers[hdr.Name] = &Layer{fs, total}

		case hdr.Name == "manifest.json":
			if err := json.NewDecoder(archive).Decode(&manifests); err != nil {
				return err
			}
		case strings.HasSuffix(hdr.Name, ".json"):
			if err := json.NewDecoder(archive).Decode(&img); err != nil {
				return err
			}
		}
	}

	manifest := manifests[0]
	history := img.History[:0]
	for _, action := range img.History {
		if !action.EmptyLayer {
			history = append(history, action)
		}
	}

	cmdWidth := *lineWidth - humanizedWidth - 4
	for i, action := range history {
		layer := layers[manifest.Layers[i]]

		var cmd string
		tokens := strings.SplitN(action.CreatedBy, "/bin/sh -c ", 2)
		if len(tokens) == 2 { // for docker build v1 case
			cmd = tokens[1]
		} else {
			cmd = action.CreatedBy
		}
		if len(cmd) > cmdWidth {
			cmd = cmd[:cmdWidth]
		}

		fmt.Println()
		fmt.Println(strings.Repeat("=", *lineWidth))
		fmt.Println(humanizeBytes(layer.Size), "\t $", strings.Replace(cmd, "\t", " ", 0))
		fmt.Println(strings.Repeat("=", *lineWidth))
		sort.Slice(layer.Files, func(i, j int) bool {
			lhs := layer.Files[i]
			rhs := layer.Files[j]
			if lhs.Size != rhs.Size {
				return lhs.Size > rhs.Size
			}
			return lhs.Name < rhs.Name
		})
		for j, f := range layer.Files {
			if j >= *maxFiles {
				break
			}
			fmt.Println(humanizeBytes(f.Size), "\t", f.Name)
		}
	}

	return nil
}

func humanizeBytes(sz int64) string {
	return pad(humanize.Bytes(uint64(sz)), humanizedWidth)
}

func pad(s string, n int) string {
	return strings.Repeat(" ", n-len(s)) + s
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

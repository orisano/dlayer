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
	"strconv"
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

func run() error {
	tarPath := flag.String("f", "-", "layer.tar path")
	maxFilesStr := flag.String("n", "10", "max files")
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
	maxFiles, err := strconv.Atoi(*maxFilesStr)
	if err != nil {
		return err
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
	_ = manifest
	history := img.History[:0]
	for _, action := range img.History {
		if !action.EmptyLayer {
			history = append(history, action)
		}
	}

	for i, action := range history {
		layer := layers[manifest.Layers[i]]

		var cmd string
		tokens := strings.SplitN(action.CreatedBy, "/bin/sh -c ", 2)
		if len(tokens) == 2 { // for docker build v1 case
			cmd = tokens[1]
		} else {
			cmd = action.CreatedBy
		}
		if len(cmd) > 100 {
			cmd = cmd[:100]
		}

		fmt.Println()
		fmt.Println(strings.Repeat("=", 130))
		fmt.Println(humanizeByte(layer.Size), "\t $", strings.Replace(cmd, "\t", " ", 0))
		fmt.Println(strings.Repeat("=", 130))
		sort.Slice(layer.Files, func(i, j int) bool {
			if layer.Files[i].Size != layer.Files[j].Size {
				return layer.Files[i].Size > layer.Files[j].Size
			}
			return layer.Files[i].Name < layer.Files[j].Name
		})
		for j, f := range layer.Files {
			if j >= maxFiles {
				break
			}
			fmt.Println(humanizeBytes(f.Size), "\t", f.Name)
		}
	}

	return nil
}

func humanizeBytes(sz int64) string {
	return pad(humanize.Bytes(uint64(sz)), 7)
}

func pad(s string, n int) string {
	return strings.Repeat(" ", n-len(s)) + s
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/dustin/go-humanize"
	"mvdan.cc/sh/syntax"
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

type FileInfo struct {
	Name string
	Size int64
}

type Layer struct {
	Files []*FileInfo
	Size  int64
}

const (
	humanizedWidth = 7
)

func run() error {
	tarPath := flag.String("f", "-", "layer.tar path")
	maxFiles := flag.Int("n", 10, "max files")
	lineWidth := flag.Int("l", 100, "screen line width")
	maxDepth := flag.Int("d", 5, "depth")
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
			sizeMap := make(map[string]int64)

			var fs []*FileInfo
			var total int64
			var name string
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
				paths := strings.Split(h.Name, "/")
				if len(paths) <= *maxDepth {
					name = strings.Join(paths, "/")
				} else {
					name = strings.Join(paths[0:*maxDepth], "/")
				}
				if _, ok := sizeMap[name]; ok {
					sizeMap[name] += h.Size
				} else {
					sizeMap[name] = h.Size
				}
				total += h.Size
			}
			for name, size := range sizeMap {
				fs = append(fs, &FileInfo{Name: name, Size: size})
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

	for i, action := range history {
		layer := layers[manifest.Layers[i]]

		var cmd string
		tokens := strings.SplitN(action.CreatedBy, "/bin/sh -c ", 2)
		if len(tokens) == 2 { // for docker build v1 case
			cmd = formatShellScript(tokens[1])
		} else {
			cmd = action.CreatedBy
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

func formatShellScript(shellScript string) string {
	parser := syntax.NewParser(syntax.KeepComments, syntax.Variant(syntax.LangPOSIX))
	prog, err := parser.Parse(strings.NewReader(shellScript), "")
	if err != nil {
		return shellScript
	}

	printer := syntax.NewPrinter(syntax.Indent(4), syntax.BinaryNextLine, syntax.SwitchCaseIndent)
	var buf bytes.Buffer
	printer.Print(&buf, prog)
	formatted := strings.TrimSuffix(buf.String(), "\n")
	if strings.Contains(formatted, "\n") {
		formatted = "# multiple line script\n" + formatted
	}
	return formatted
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

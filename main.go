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

type FileInfo struct {
	Name string
	Size int64
}

type Layer struct {
	ID        string
	CreatedBy string
	Files     []*FileInfo
}

type Image struct {
	Layers []Layer
}

const (
	humanizedWidth = 7
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	tarPath := flag.String("f", "-", "layer.tar path")
	maxFiles := flag.Int("n", 10, "max files")
	lineWidth := flag.Int("l", 100, "screen line width")
	maxDepth := flag.Int("d", 5, "depth")
	flag.Parse()

	rc, err := openStream(*tarPath)
	if err != nil {
		return err
	}
	layers, err := readLayers(rc)
	if err != nil {
		return err
	}
	for _, layer := range layers {
		var cmd string
		tokens := strings.SplitN(layer.CreatedBy, "/bin/sh -c ", 2)
		if len(tokens) == 2 { // for docker build v1 case
			cmd = formatShellScript(tokens[1])
		} else {
			cmd = layer.CreatedBy
		}

		layerSize := int64(0)
		outputMap := make(map[string]int64)
		for _, f := range layer.Files {
			layerSize += f.Size

			tokens := strings.Split(f.Name, "/")
			if len(tokens) > *maxDepth {
				tokens = tokens[:*maxDepth]
			}
			key := strings.Join(tokens, "/")

			outputMap[key] += f.Size
		}

		files := make([]*FileInfo, 0, len(outputMap))
		for k, v := range outputMap {
			files = append(files, &FileInfo{
				Name: k,
				Size: v,
			})
		}

		fmt.Println()
		fmt.Println(strings.Repeat("=", *lineWidth))
		fmt.Println(humanizeBytes(layerSize), "\t $", cmd)
		fmt.Println(strings.Repeat("=", *lineWidth))
		sort.Slice(files, func(i, j int) bool {
			lhs := files[i]
			rhs := files[j]
			if lhs.Size != rhs.Size {
				return lhs.Size > rhs.Size
			}
			return lhs.Name < rhs.Name
		})
		for j, f := range files {
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

func readLayers(rc io.ReadCloser) ([]*Layer, error) {
	defer rc.Close()

	var manifests []struct {
		Config   string
		RepoTags []string
		Layers   []string
	}
	var imageMeta struct {
		History []struct {
			EmptyLayer bool   `json:"empty_layer,omitempty"`
			CreatedBy  string `json:"created_by,omitempty"`
		} `json:"history,omitempty"`
	}
	files := make(map[string][]*FileInfo)

	archive := tar.NewReader(rc)
	for {
		hdr, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		switch {
		case strings.HasSuffix(hdr.Name, "/layer.tar"):
			fs, err := readFiles(archive)
			if err != nil {
				return nil, err
			}
			files[hdr.Name] = fs
		case hdr.Name == "manifest.json":
			if err := json.NewDecoder(archive).Decode(&manifests); err != nil {
				return nil, err
			}
		case strings.HasSuffix(hdr.Name, ".json"):
			if err := json.NewDecoder(archive).Decode(&imageMeta); err != nil {
				return nil, err
			}
		}
	}

	manifest := manifests[0]
	history := imageMeta.History[:0]
	for _, layer := range imageMeta.History {
		if !layer.EmptyLayer {
			history = append(history, layer)
		}
	}

	var layers []*Layer
	for i, layer := range history {
		name := manifest.Layers[i]
		fs := files[name]
		layers = append(layers, &Layer{
			ID:        strings.Split(name, "/")[0],
			CreatedBy: layer.CreatedBy,
			Files:     fs,
		})
	}

	return layers, nil
}

func readFiles(r io.Reader) ([]*FileInfo, error) {
	var files []*FileInfo
	archive := tar.NewReader(r)
	for {
		hdr, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		fi := hdr.FileInfo()
		if fi.IsDir() {
			continue
		}
		files = append(files, &FileInfo{
			Name: hdr.Name,
			Size: fi.Size(),
		})
	}
	return files, nil
}

func openStream(path string) (io.ReadCloser, error) {
	if path == "-" {
		return os.Stdin, nil
	} else {
		return os.Open(path)
	}
}

func humanizeBytes(sz int64) string {
	return pad(humanize.Bytes(uint64(sz)), humanizedWidth)
}

func pad(s string, n int) string {
	return strings.Repeat(" ", n-len(s)) + s
}

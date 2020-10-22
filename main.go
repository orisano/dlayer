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
	"os/exec"
	"sort"
	"strings"

	"github.com/dustin/go-humanize"
	"github.com/gdamore/tcell"
	"github.com/pkg/profile"
	"github.com/rivo/tview"
	"mvdan.cc/sh/v3/syntax"
)

type FileInfo struct {
	Name    string
	Size    int64
	Details *FileDetails
}

type FileDetails struct {
	FileMode os.FileMode
	Uid      int
	Gid      int
}

type Layer struct {
	ID        string
	CreatedBy string
	Files     []*FileInfo
}

type Image struct {
	Tags   []string
	Layers []*Layer
}

const (
	humanizedWidth = 7
)

func main() {
	if os.Getenv("DLAYER_PROFILE") != "" {
		defer profile.Start().Stop()
	}
	log.SetFlags(0)
	log.SetPrefix("dlayer: ")
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	tarPath := flag.String("f", "-", "image.tar path")
	maxFiles := flag.Int("n", 100, "max files")
	lineWidth := flag.Int("l", 100, "screen line width")
	maxDepth := flag.Int("d", 8, "max depth")
	all := flag.Bool("a", false, "show details")
	interactive := flag.Bool("i", false, "interactive mode")
	flag.Parse()

	if *interactive {
		locale := getLocale()
		if locale != "" && locale != "en_US.UTF-8" {
			binPath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("get executable: %w", err)
			}
			cmd := exec.Command(binPath, os.Args[1:]...)
			cmd.Stdin = os.Stdin
			cmd.Stderr = os.Stderr
			cmd.Stdout = os.Stdout
			cmd.Env = append(os.Environ(), `LC_CTYPE=en_US.UTF-8`)
			return cmd.Run()
		}
	}

	rc, err := openStream(*tarPath)
	if err != nil {
		return fmt.Errorf("open tar: %w", err)
	}
	img, err := readImage(rc)
	if err != nil {
		return fmt.Errorf("read image: %w", err)
	}

	if *interactive {
		return runInteractive(img)
	}

	for _, layer := range img.Layers {
		var cmd string
		tokens := strings.SplitN(layer.CreatedBy, "/bin/sh -c ", 2)
		if len(tokens) == 2 { // for docker build v1 case
			cmd = formatShellScript(tokens[1])
		} else {
			cmd = layer.CreatedBy
		}

		layerSize := int64(0)
		outputMap := make(map[string]int64)
		byName := make(map[string]*FileInfo)
		for _, f := range layer.Files {
			byName[f.Name] = f

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
			fi := &FileInfo{
				Name: k,
				Size: v,
			}
			if f, ok := byName[k]; ok {
				fi.Details = f.Details
			}
			files = append(files, fi)
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
			if *all {
				if f.Details != nil {
					fmt.Println(humanizeBytes(f.Size), fmt.Sprintf("%5d:%-5d", f.Details.Gid, f.Details.Uid), f.Details.FileMode.String(), f.Name)
				} else {
					fmt.Println(humanizeBytes(f.Size), strings.Repeat(" ", 22), f.Name)
				}
			} else {
				fmt.Println(humanizeBytes(f.Size), "\t", f.Name)
			}
		}
	}

	return nil
}

func formatShellScript(shellScript string) string {
	parser := syntax.NewParser(syntax.KeepComments(true), syntax.Variant(syntax.LangPOSIX))
	prog, err := parser.Parse(strings.NewReader(shellScript), "")
	if err != nil {
		return shellScript
	}

	printer := syntax.NewPrinter(syntax.Indent(4), syntax.BinaryNextLine(true), syntax.SwitchCaseIndent(true))
	var buf bytes.Buffer
	printer.Print(&buf, prog)
	formatted := strings.TrimSuffix(buf.String(), "\n")
	if strings.Contains(formatted, "\n") {
		formatted = "# multiple line script\n" + formatted
	}
	return formatted
}

func readImage(rc io.ReadCloser) (*Image, error) {
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
			return nil, fmt.Errorf("next: %w", err)
		}

		switch {
		case strings.HasSuffix(hdr.Name, "/layer.tar"):
			fs, err := readFiles(archive)
			if err != nil {
				return nil, fmt.Errorf("read layer(%s): %w", hdr.Name, err)
			}
			files[hdr.Name] = fs
		case hdr.Name == "manifest.json":
			if err := json.NewDecoder(archive).Decode(&manifests); err != nil {
				return nil, fmt.Errorf("decode manifest: %w", err)
			}
		case strings.HasSuffix(hdr.Name, ".json"):
			if err := json.NewDecoder(archive).Decode(&imageMeta); err != nil {
				return nil, fmt.Errorf("decode meta(%s): %w", hdr.Name, err)
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

	return &Image{
		Tags:   manifest.RepoTags,
		Layers: layers,
	}, nil
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
			return nil, fmt.Errorf("next: %w", err)
		}
		fi := hdr.FileInfo()
		if fi.IsDir() {
			continue
		}
		files = append(files, &FileInfo{
			Name: hdr.Name,
			Size: fi.Size(),
			Details: &FileDetails{
				FileMode: fi.Mode().Perm(),
				Uid:      hdr.Uid,
				Gid:      hdr.Gid,
			},
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
	if sz < 1000 {
		return pad(fmt.Sprintf("%d  B", sz), humanizedWidth)
	} else {
		return pad(humanize.Bytes(uint64(sz)), humanizedWidth)
	}
}

func pad(s string, n int) string {
	return strings.Repeat(" ", n-len(s)) + s
}

func getLocale() string {
	ctype := os.Getenv("LC_CTYPE")
	if ctype != "" {
		return ctype
	}
	return os.Getenv("LANG")
}

func runInteractive(img *Image) error {
	app := tview.NewApplication()

	rootDir := strings.Join(img.Tags, ", ")
	root := tview.NewTreeNode(rootDir)
	tree := tview.NewTreeView().
		SetRoot(root).
		SetCurrentNode(root)

	for _, layer := range img.Layers {
		text := strings.TrimPrefix(layer.CreatedBy, "/bin/sh -c ")
		switch {
		case strings.HasPrefix(text, "RUN "):
		case strings.HasPrefix(text, "COPY "):
		case strings.HasPrefix(text, "ADD "):
		case strings.HasPrefix(text, "WORKDIR "):
		case strings.HasPrefix(text, "#(nop) "):
			text = strings.TrimPrefix(text, "#(nop) ")
		default:
			text = "RUN " + text
		}
		tn := tview.NewTreeNode(text)
		addFiles(tn, layer.Files, true)
		root.AddChild(tn)
	}

	tree.SetSelectedFunc(func(node *tview.TreeNode) {
		open := !node.IsExpanded()
		node.SetExpanded(open)
		if open {
			children := node.GetChildren()
			for len(children) == 1 {
				child := children[0]
				child.SetExpanded(true)
				children = child.GetChildren()
			}
		}
	})

	tree.SetInputCapture(func(e *tcell.EventKey) *tcell.EventKey {
		if e.Rune() == 'q' {
			app.Stop()
		}
		if e.Rune() == 'u' {
			parent, ok := tree.GetCurrentNode().GetReference().(*tview.TreeNode)
			if ok && parent != nil {
				parent.SetExpanded(false)
				tree.SetCurrentNode(parent)
			}
		}
		return e
	})

	return app.SetRoot(tree, true).Run()
}

func addFiles(node *tview.TreeNode, files []*FileInfo, root bool) int64 {
	tree := make(map[string][]*FileInfo)
	size := int64(0)
	for _, f := range files {
		size += f.Size
		if f.Name == "" {
			continue
		}
		xs := strings.SplitN(f.Name, "/", 2)
		key := xs[0]
		child := ""
		if len(xs) == 2 {
			child = xs[1]
		}
		tree[key] = append(tree[key], &FileInfo{Name: child, Size: f.Size})
	}

	type entry struct {
		node *tview.TreeNode
		size int64
	}
	entries := make([]*entry, 0, len(tree))
	for key := range tree {
		t := tview.NewTreeNode(key)
		s := addFiles(t, tree[key], false)
		entries = append(entries, &entry{
			node: t,
			size: s,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].size > entries[j].size
	})
	for _, e := range entries {
		node.AddChild(e.node)
		e.node.SetReference(node)
	}
	text := humanizeBytes(size) + ": " + node.GetText()
	if !root && len(entries) > 0 {
		text += "/"
	}
	node.SetText(text)
	node.SetExpanded(false)
	return size
}

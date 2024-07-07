package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/dustin/go-humanize"
	"github.com/gdamore/tcell/v2"
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
	FileName  string
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
	log.SetFlags(0)
	log.SetPrefix("dlayer: ")
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if os.Getenv("DLAYER_PROFILE") != "" {
		defer profile.Start().Stop()
	}
	tarPath := flag.String("f", "-", "image.tar path")
	maxFiles := flag.Int("n", 100, "max files")
	lineWidth := flag.Int("l", 100, "screen line width")
	maxDepth := flag.Int("d", 8, "max depth")
	all := flag.Bool("a", false, "show details")
	interactive := flag.Bool("i", false, "interactive mode")
	defaultArch := flag.String("arch", getEnv("DLAYER_ARCH", runtime.GOARCH), "default architecture")
	tag := flag.String("tag", "", "tag")
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
	img, err := readImage(rc, *tag, *defaultArch)
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

func readImage(rc io.ReadCloser, tag, arch string) (*Image, error) {
	defer rc.Close()

	type Manifest struct {
		Config   string
		RepoTags []string
		Layers   []string
	}
	var manifests []Manifest
	type Config struct {
		Architecture string `json:"architecture"`
		History      []struct {
			EmptyLayer bool   `json:"empty_layer,omitempty"`
			CreatedBy  string `json:"created_by,omitempty"`
		} `json:"history,omitempty"`
	}
	configs := make(map[string]*Config)
	files := make(map[string][]*FileInfo)
	var r io.Reader = rc
	if bufSize := os.Getenv("DLAYER_BUFFER_SIZE"); bufSize != "" {
		bufBytes, err := humanize.ParseBytes(bufSize)
		if err != nil {
			return nil, fmt.Errorf("parse buffer size: %w", err)
		}
		r = bufio.NewReaderSize(r, int(bufBytes))
	}
	archive := tar.NewReader(r)
	for {
		hdr, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("next: %w", err)
		}
		if hdr.FileInfo().IsDir() {
			continue
		}

		isTar := false
		var ar io.Reader = archive
		if strings.HasPrefix(hdr.Name, "blobs/") {
			var head [262]byte
			_, err := io.ReadFull(archive, head[:])
			if err == nil {
				isTar = bytes.HasSuffix(head[:], []byte("ustar"))
				ar = io.MultiReader(bytes.NewReader(head[:]), archive)
			} else if !errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, fmt.Errorf("read blob(%s): %w", hdr.Name, err)
			}
		}

		switch {
		case hdr.Name == "manifest.json":
			if err := json.NewDecoder(ar).Decode(&manifests); err != nil {
				return nil, fmt.Errorf("decode manifest: %w", err)
			}
		case strings.HasSuffix(hdr.Name, "/layer.tar") || isTar:
			fs, err := readFiles(ar)
			if err != nil {
				return nil, fmt.Errorf("read layer(%s): %w", hdr.Name, err)
			}
			files[hdr.Name] = fs
		case strings.HasSuffix(hdr.Name, ".json"):
			var config Config
			if err := json.NewDecoder(ar).Decode(&config); err != nil {
				return nil, fmt.Errorf("decode meta(%s): %w", hdr.Name, err)
			}
			configs[hdr.Name] = &config
		default:
			var config Config
			if err := json.NewDecoder(ar).Decode(&config); err == nil {
				configs[hdr.Name] = &config
			}
		}
	}

	if len(manifests) == 0 {
		return nil, fmt.Errorf("manifest.json not found")
	}

	var manifest *Manifest
	if len(manifests) == 1 {
		manifest = &manifests[0]
	} else {
		for i := range manifests {
			if tag != "" {
				matched := false
				for _, t := range manifests[i].RepoTags {
					if strings.HasSuffix(t, ":"+tag) {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}
			if configs[manifests[i].Config].Architecture == arch {
				manifest = &manifests[i]
				break
			}
		}
		if manifest == nil {
			log.Print("available manifests:")
			for _, m := range manifests {
				for _, t := range m.RepoTags {
					log.Println("-", t, configs[m.Config].Architecture)
				}
			}
			return nil, fmt.Errorf("config not found(arch=%s)", arch)
		}
	}
	config := configs[manifest.Config]
	history := config.History[:0]
	for _, layer := range config.History {
		if !layer.EmptyLayer {
			history = append(history, layer)
		}
	}

	var layers []*Layer
	for i, layer := range history {
		name := manifest.Layers[i]
		fs := files[name]
		layers = append(layers, &Layer{
			FileName:  name,
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
			Name: filepath.Clean(hdr.Name),
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

func getEnv(key, v string) string {
	x := os.Getenv(key)
	if x == "" {
		return v
	}
	return x
}

func runInteractive(img *Image) error {
	app := tview.NewApplication()

	rootDir := strings.Join(img.Tags, ", ")
	root := tview.NewTreeNode(rootDir)
	tree := tview.NewTreeView().
		SetRoot(root).
		SetCurrentNode(root)
	navi := tview.NewTextView()

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
		tn.SetReference(layer)
		addFiles(tn, layer.Files, nil)
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
		switch e.Rune() {
		case 'q':
			app.Stop()
		case 'u':
			node, ok := tree.GetCurrentNode().GetReference().(*TreeNode)
			if ok && node.parent != nil {
				node.parent.value.SetExpanded(false)
				tree.SetCurrentNode(node.parent.value)
			}
		case 'y':
			_ = clipboard.WriteAll(navi.GetText(true))
		}
		return e
	})

	tree.SetChangedFunc(func(target *tview.TreeNode) {
		node, ok := target.GetReference().(*TreeNode)
		if ok {
			navi.SetText(node.ExtractCommand())
		} else {
			navi.SetText("")
		}
	})
	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(tree, 0, 1, true).
		AddItem(navi, 1, 0, false)
	return app.SetRoot(flex, true).SetFocus(flex).Run()
}

type TreeNode struct {
	layerFile string
	parent    *TreeNode
	value     *tview.TreeNode
	key       string
	dir       bool
}

func (n *TreeNode) Path() string {
	if n.parent == nil {
		return n.key
	}
	return n.parent.Path() + "/" + n.key
}

func (n *TreeNode) ExtractCommand() string {
	layerCmd := "tar xO " + n.layerFile
	if n.dir {
		return layerCmd + " | tar x " + n.Path()
	} else {
		return layerCmd + " | tar xO " + n.Path()
	}
}

func addFiles(node *tview.TreeNode, files []*FileInfo, parent *TreeNode) int64 {
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
		node *TreeNode
		size int64
	}
	entries := make([]*entry, 0, len(tree))
	for key := range tree {
		t := tview.NewTreeNode(key)
		child := &TreeNode{
			parent: parent,
			value:  t,
			key:    key,
		}
		if parent != nil {
			child.layerFile = parent.layerFile
		} else {
			child.layerFile = node.GetReference().(*Layer).FileName
		}
		s := addFiles(t, tree[key], child)
		entries = append(entries, &entry{
			node: child,
			size: s,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].size > entries[j].size
	})
	for _, e := range entries {
		node.AddChild(e.node.value)
		e.node.value.SetReference(e.node)
	}
	text := humanizeBytes(size) + ": " + node.GetText()
	if parent != nil && len(entries) > 0 {
		text += "/"
		parent.dir = true
	}
	node.SetText(text)
	node.SetExpanded(false)
	return size
}

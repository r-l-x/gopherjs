package main

import (
	"flag"
	"fmt"
	"go/build"
	"go/parser"
	"go/scanner"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path"
)

type Translator struct {
	buildContext *build.Context
	packages     map[string]*GopherPackage
}

func main() {
	var pkg *GopherPackage
	var out io.Writer

	fileSet := token.NewFileSet()
	t := &Translator{
		packages: make(map[string]*GopherPackage),
		buildContext: &build.Context{
			GOROOT:        build.Default.GOROOT,
			GOPATH:        build.Default.GOPATH,
			GOOS:          build.Default.GOOS,
			GOARCH:        build.Default.GOARCH,
			Compiler:      "gc",
			InstallSuffix: "js",
		},
	}
	t.packages["reflect"] = &GopherPackage{Package: &build.Package{}}
	t.packages["go/doc"] = &GopherPackage{Package: &build.Package{}}

	flag.Parse()

	switch flag.Arg(0) {
	case "install":
		buildPkg, err := t.buildContext.Import(flag.Arg(1), "", 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
		pkg = &GopherPackage{Package: buildPkg}
		pkg.PkgObj = pkg.BinDir + "/" + path.Base(pkg.ImportPath) + ".js"

	case "build", "run":
		filename := flag.Arg(1)
		file, err := parser.ParseFile(fileSet, filename, nil, parser.ImportsOnly)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}

		imports := make([]string, len(file.Imports))
		for i, imp := range file.Imports {
			imports[i] = imp.Path.Value[1 : len(imp.Path.Value)-1]
		}

		basename := path.Base(filename)
		pkg = &GopherPackage{
			Package: &build.Package{
				Name:    "main",
				Imports: imports,
				Dir:     path.Dir(filename),
				GoFiles: []string{basename},
				PkgObj:  basename[:len(basename)-3] + ".js",
			},
		}

		if flag.Arg(0) == "run" {
			node := exec.Command("node")
			pipe, _ := node.StdinPipe()
			out = pipe
			node.Stdout = os.Stdout
			node.Stderr = os.Stderr
			err = node.Start()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return
			}
			defer node.Wait()
			defer pipe.Close()
		}

	case "help", "":
		os.Stderr.WriteString(`GopherJS is a tool for compiling Go source code to JavaScript.

Usage:

    gopherjs command [arguments]

The commands are:

    build       compile packages and dependencies
    install     compile and install packages and dependencies
    run         compile and run Go program

`)
		return

	default:
		fmt.Fprintf(os.Stderr, "gopherjs: unknown subcommand \"%s\"\nRun 'gopherjs help' for usage.\n", flag.Arg(0))
		return
	}

	err := t.buildPackage(pkg, fileSet, out)
	if err != nil {
		list, isList := err.(scanner.ErrorList)
		if !isList {
			fmt.Fprintln(os.Stderr, err)
			return
		}
		for _, entry := range list {
			fmt.Fprintln(os.Stderr, entry)
		}
	}
}

func (t *Translator) buildPackage(pkg *GopherPackage, fileSet *token.FileSet, out io.Writer) error {
	fileInfo, err := os.Stat(os.Args[0]) // gopherjs itself
	if err != nil {
		return err
	}
	pkg.SrcLastModified = fileInfo.ModTime()

	pkg.ImportedPackages = make([]*GopherPackage, len(pkg.Imports))
	for i, importedPkg := range pkg.Imports {
		if _, found := t.packages[importedPkg]; !found {
			otherPkg, err := t.buildContext.Import(importedPkg, pkg.Dir, 0)
			if err != nil {
				return err
			}
			if err := t.buildPackage(&GopherPackage{Package: otherPkg}, fileSet, nil); err != nil {
				return err
			}
		}

		compiledPkg := t.packages[importedPkg]
		pkg.ImportedPackages[i] = compiledPkg
		if compiledPkg.SrcLastModified.After(pkg.SrcLastModified) {
			pkg.SrcLastModified = compiledPkg.SrcLastModified
		}
	}

	for _, name := range pkg.GoFiles {
		fileInfo, err := os.Stat(pkg.Dir + "/" + name)
		if err != nil {
			return err
		}
		if fileInfo.ModTime().After(pkg.SrcLastModified) {
			pkg.SrcLastModified = fileInfo.ModTime()
		}
	}

	t.packages[pkg.ImportPath] = pkg

	fileInfo, err = os.Stat(pkg.PkgObj)
	if err == nil {
		if fileInfo.ModTime().After(pkg.SrcLastModified) {
			return nil
		}
	}

	if err := pkg.translate(fileSet); err != nil {
		return err
	}

	if out != nil {
		pkg.JavaScriptCode.WriteTo(out)
		return nil
	}

	if err := os.MkdirAll(path.Dir(pkg.PkgObj), 0777); err != nil {
		return err
	}
	var perm os.FileMode = 0666
	if pkg.IsCommand() {
		perm = 0777
	}
	file, err := os.OpenFile(pkg.PkgObj, os.O_RDWR|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if pkg.IsCommand() {
		file.Write([]byte("#!/usr/bin/env node\n"))
	}
	pkg.JavaScriptCode.WriteTo(file)
	file.Close()
	return nil
}
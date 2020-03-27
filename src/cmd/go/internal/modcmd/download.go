// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package modcmd

import (
	"encoding/json"
	"os"

	"cmd/go/internal/base"
	"cmd/go/internal/cfg"
	"cmd/go/internal/modfetch"
	"cmd/go/internal/modload"
	"cmd/go/internal/par"
	"cmd/go/internal/work"

	"golang.org/x/mod/module"
)

var cmdDownload = &base.Command{
	UsageLine: "go mod download [-x] [-json] [modules]",
	Short:     "download modules to local cache",
	Long: `
Download downloads the named modules, which can be module patterns selecting
dependencies of the main module or module queries of the form path@version.
With no arguments, download applies to all dependencies of the main module.

The go command will automatically download modules as needed during ordinary
execution. The "go mod download" command is useful mainly for pre-filling
the local cache or to compute the answers for a Go module proxy.

By default, download writes nothing to standard output. It may print progress
messages and errors to standard error.

The -json flag causes download to print a sequence of JSON objects
to standard output, describing each downloaded module (or failure),
corresponding to this Go struct:

    type Module struct {
        Path     string // module path
        Version  string // module version
        Error    string // error loading module
        Info     string // absolute path to cached .info file
        GoMod    string // absolute path to cached .mod file
        Zip      string // absolute path to cached .zip file
        Dir      string // absolute path to cached source root directory
        Sum      string // checksum for path, version (as in go.sum)
        GoModSum string // checksum for go.mod (as in go.sum)
    }

The -x flag causes download to print the commands download executes.

See 'go help modules' for more about module queries.
	`,
}

var downloadJSON = cmdDownload.Flag.Bool("json", false, "")

func init() {
	cmdDownload.Run = runDownload // break init cycle

	// TODO(jayconrod): https://golang.org/issue/35849 Apply -x to other 'go mod' commands.
	cmdDownload.Flag.BoolVar(&cfg.BuildX, "x", false, "")
	work.AddModCommonFlags(cmdDownload)
}

type moduleJSON struct {
	Path     string `json:",omitempty"`
	Version  string `json:",omitempty"`
	Error    string `json:",omitempty"`
	Info     string `json:",omitempty"`
	GoMod    string `json:",omitempty"`
	Zip      string `json:",omitempty"`
	Dir      string `json:",omitempty"`
	Sum      string `json:",omitempty"`
	GoModSum string `json:",omitempty"`
}

func runDownload(cmd *base.Command, args []string) {
	// Check whether modules are enabled and whether we're in a module.
	if cfg.Getenv("GO111MODULE") == "off" {
		base.Fatalf("go: modules disabled by GO111MODULE=off; see 'go help modules'")
	}
	if !modload.HasModRoot() && len(args) == 0 {
		base.Fatalf("go mod download: no modules specified (see 'go help mod download')")
	}
	if len(args) == 0 {
		args = []string{"all"}
		// modload.HasModRoot 判断当前是否gomod模式，当前文件夹是否有go.mod作为一个module的根目录
	} else if modload.HasModRoot() {
		// modload.InitMod 解析了当前文件夹下的go.mod文件，把go.mod解析到modfile.File结构体中
		// 并且将结构体实体赋值到modload.Target
		// module/module.Version 结构体的具体含义是go.mod 文件中的每一行的每一个module
		modload.InitMod() // to fill Target
		targetAtLatest := modload.Target.Path + "@latest"
		targetAtUpgrade := modload.Target.Path + "@upgrade"
		targetAtPatch := modload.Target.Path + "@patch"
		// 如果download的参数里有go.mod声明的main module， 则报错。因为不能&也没用意义去下载main module
		for _, arg := range args {
			switch arg {
			case modload.Target.Path, targetAtLatest, targetAtUpgrade, targetAtPatch:
				os.Stderr.WriteString("go mod download: skipping argument " + arg + " that resolves to the main module\n")
			}
		}
	}

	// module 的下载情况记录
	var mods []*moduleJSON
	// work 并发下载任务管理?
	var work par.Work
	listU := false
	listVersions := false
	// 通过 	buildList, err = mvs.BuildList(Target, reqs)
	// 使用mvs最小版本选择包的函数解析 target下path 的所有imports ？
	//
	for _, info := range modload.ListModules(args, listU, listVersions) {
		if info.Replace != nil {
			info = info.Replace
		}
		if info.Version == "" && info.Error == nil {
			// main module or module replaced with file path.
			// Nothing to download.
			continue
		}
		m := &moduleJSON{
			Path:    info.Path,
			Version: info.Version,
		}
		mods = append(mods, m)
		if info.Error != nil {
			m.Error = info.Error.Err
			continue
		}
		work.Add(m)
	}

	work.Do(10, func(item interface{}) {
		m := item.(*moduleJSON)
		var err error
		m.Info, err = modfetch.InfoFile(m.Path, m.Version)
		if err != nil {
			m.Error = err.Error()
			return
		}
		m.GoMod, err = modfetch.GoModFile(m.Path, m.Version)
		if err != nil {
			m.Error = err.Error()
			return
		}
		m.GoModSum, err = modfetch.GoModSum(m.Path, m.Version)
		if err != nil {
			m.Error = err.Error()
			return
		}
		mod := module.Version{Path: m.Path, Version: m.Version}
		m.Zip, err = modfetch.DownloadZip(mod)
		if err != nil {
			m.Error = err.Error()
			return
		}
		m.Sum = modfetch.Sum(mod)
		m.Dir, err = modfetch.Download(mod)
		if err != nil {
			m.Error = err.Error()
			return
		}
	})

	if *downloadJSON {
		for _, m := range mods {
			b, err := json.MarshalIndent(m, "", "\t")
			if err != nil {
				base.Fatalf("%v", err)
			}
			os.Stdout.Write(append(b, '\n'))
			if m.Error != "" {
				base.SetExitStatus(1)
			}
		}
	} else {
		for _, m := range mods {
			if m.Error != "" {
				base.Errorf("%s", m.Error)
			}
		}
		base.ExitIfErrors()
	}
}

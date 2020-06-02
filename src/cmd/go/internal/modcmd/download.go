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
		// gomod 第一行 信息 解析到Target中
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

	// module 的解析结构体
	var mods []*moduleJSON
	// work 并发任务定义结构体
	var work par.Work
	listU := false
	listVersions := false
	// 获取go.mod所有的模块，如果命令行参数有指定则会进行匹配，如果没有，则直接就是全部
	// 并且把go.mod匹配的模块都进行更加详细的info查询，
	// type ModulePublic struct {
	//    Path      string        `json:",omitempty"` // module path
	//    Version   string        `json:",omitempty"` // module version
	//    Versions  []string      `json:",omitempty"` // available module versions
	//    Replace   *ModulePublic `json:",omitempty"` // replaced by this module
	//    Time      *time.Time    `json:",omitempty"` // time version was created
	//    Update    *ModulePublic `json:",omitempty"` // available update (with -u)
	//    Main      bool          `json:",omitempty"` // is this the main module?
	//    Indirect  bool          `json:",omitempty"` // module is only indirectly needed by main module
	//    Dir       string        `json:",omitempty"` // directory holding local copy of files, if any
	//    GoMod     string        `json:",omitempty"` // path to go.mod file describing module, if any
	//    GoVersion string        `json:",omitempty"` // go version used in module
	//    Error     *ModuleError  `json:",omitempty"` // error loading module
	//}

	for _, info := range modload.ListModules(args, listU, listVersions) {
		// 判断是否有replace对应到当前的module，如果有则替换后加入modsJSON。
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
		// 解析完成的任务加入work结构体任务数据
		work.Add(m)
	}

	// 执行work结构体所制定的Do函数，指定并发数和执行函数
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

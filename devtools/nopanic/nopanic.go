// Package nopanic is a go/analysis pass for the Craft never-crash standard: production code
// must not crash on purpose. It reports, in non-test, non-generated files:
//
//   - calls to the builtin panic;
//   - calls to functions or methods named Must… (regexp.MustCompile, template.Must, …) unless
//     every argument is a compile-time constant, so a bad value can only come from the source;
//   - log.Panic, log.Panicf, log.Panicln, log.Fatal, log.Fatalf and log.Fatalln, on the log
//     package or a *log.Logger: they panic or exit without saving the user's work;
//   - os.Exit outside package main.
//
// Go can still panic implicitly (nil dereference, index out of range); those are handled by
// recover at the app's boundaries, bounds checks and fuzzing, not by this pass.
package nopanic

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// Analyzer reports deliberate crash paths in production code.
var Analyzer = &analysis.Analyzer{
	Name:     "nopanic",
	Doc:      "report panic, non-constant Must… calls, log.Panic*/log.Fatal* and os.Exit outside main in non-test code",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

func run(pass *analysis.Pass) (any, error) {
	insp, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return nil, nil
	}
	skip := map[*ast.File]bool{}
	for _, f := range pass.Files {
		name := pass.Fset.File(f.Pos()).Name()
		if strings.HasSuffix(name, "_test.go") || ast.IsGenerated(f) {
			skip[f] = true
		}
	}
	insp.WithStack([]ast.Node{(*ast.CallExpr)(nil)}, func(n ast.Node, push bool, stack []ast.Node) bool {
		if !push || len(stack) == 0 {
			return true
		}
		if f, ok := stack[0].(*ast.File); ok && skip[f] {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		check(pass, call)
		return true
	})
	return nil, nil
}

func check(pass *analysis.Pass, call *ast.CallExpr) {
	switch obj := callee(pass.TypesInfo, call.Fun).(type) {
	case *types.Builtin:
		if obj.Name() == "panic" {
			pass.Reportf(call.Pos(), "panic in production code: return an error instead")
		}
	case *types.Func:
		name := obj.Name()
		pkg := ""
		if obj.Pkg() != nil {
			pkg = obj.Pkg().Path()
		}
		switch {
		case strings.HasPrefix(name, "Must") && !allConstant(pass.TypesInfo, call.Args):
			pass.Reportf(call.Pos(), "%s panics on failure: handle the error instead", name)
		case pkg == "log" && (strings.HasPrefix(name, "Panic") || strings.HasPrefix(name, "Fatal")):
			pass.Reportf(call.Pos(), "log.%s crashes or exits without saving: log and return an error instead", name)
		case pkg == "os" && name == "Exit" && pass.Pkg.Name() != "main":
			pass.Reportf(call.Pos(), "os.Exit outside package main: return an error to the caller instead")
		}
	}
}

// callee resolves the object a call expression calls, through parentheses, selectors and
// generic instantiations.
func callee(info *types.Info, fun ast.Expr) types.Object {
	for {
		switch e := fun.(type) {
		case *ast.ParenExpr:
			fun = e.X
		case *ast.IndexExpr:
			fun = e.X
		case *ast.IndexListExpr:
			fun = e.X
		case *ast.Ident:
			return info.Uses[e]
		case *ast.SelectorExpr:
			if sel, ok := info.Selections[e]; ok {
				return sel.Obj()
			}
			return info.Uses[e.Sel]
		default:
			return nil
		}
	}
}

func allConstant(info *types.Info, args []ast.Expr) bool {
	for _, a := range args {
		tv, ok := info.Types[a]
		if !ok || tv.Value == nil {
			return false
		}
	}
	return true
}

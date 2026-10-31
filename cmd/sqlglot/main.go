// Command sqlglot transpiles SQL between dialects (a port of `python -m sqlglot`).
//
// Usage:
//
//	sqlglot [--read DIALECT] [--write DIALECT] [--identify safe|true|false] [--no-pretty]
//	        [--parse | --tokenize] [--error-level LEVEL] SQL
//
// SQL is the statement(s) to transpile, or - to read stdin.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	sqlglot "github.com/cottrellashley/sqlglot-go"
	_ "github.com/cottrellashley/sqlglot-go/dialects"
	"github.com/cottrellashley/sqlglot-go/errs"
	"github.com/cottrellashley/sqlglot-go/exp"
	"github.com/cottrellashley/sqlglot-go/helper"
)

// Version is the version reported by --version.
var Version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("sqlglot", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: sqlglot [flags] sql\n\nTranspile SQL. sql is the SQL statement(s) to transpile, or - to read stdin.")
		fs.PrintDefaults()
	}
	read := fs.String("read", "", "Dialect to read; default is generic")
	write := fs.String("write", "", "Dialect to write; default is generic")
	identify := fs.String("identify", "safe", "Whether to quote identifiers (safe, true, false)")
	noPretty := fs.Bool("no-pretty", false, "Compress sql")
	parse := fs.Bool("parse", false, "Parse and return the expression tree")
	tokenize := fs.Bool("tokenize", false, "Tokenize and return the tokens list")
	errorLevel := fs.String("error-level", "IMMEDIATE", "IGNORE, WARN, RAISE, IMMEDIATE (default)")
	version := fs.Bool("version", false, "Display the version")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *version {
		fmt.Fprintln(stdout, Version)
		return nil
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("expected one sql argument")
	}
	level, err := parseErrorLevel(*errorLevel)
	if err != nil {
		return err
	}
	sql := fs.Arg(0)
	if sql == "-" {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		sql = string(b)
	}
	readDialect := dialectArg(*read)

	switch {
	case *parse:
		d, err := sqlglot.GetDialect(readDialect)
		if err != nil {
			return err
		}
		expressions, err := d.Parse(sql, sqlglot.ParserOptions{ErrorLevel: level})
		if err != nil {
			return err
		}
		for _, e := range expressions {
			if e == nil {
				fmt.Fprintln(stdout, "None")
			} else {
				fmt.Fprintln(stdout, e.Repr())
			}
		}
	case *tokenize:
		toks, err := sqlglot.Tokenize(sql, readDialect)
		if err != nil {
			return err
		}
		for _, t := range toks {
			fmt.Fprintln(stdout, t)
		}
	default:
		var id any = "safe"
		if *identify != "safe" {
			id = helper.ToBool(*identify)
		}
		out, err := sqlglot.TranspileOpts(sql, readDialect, dialectArg(*write), sqlglot.ParserOptions{ErrorLevel: level},
			exp.Identify(id), exp.Pretty(!*noPretty))
		if err != nil {
			return err
		}
		for _, s := range out {
			fmt.Fprintln(stdout, s)
		}
	}
	return nil
}

func dialectArg(name string) any {
	if name == "" {
		return nil
	}
	return name
}

func parseErrorLevel(s string) (errs.ErrorLevel, error) {
	switch strings.ToUpper(s) {
	case "IGNORE":
		return errs.Ignore, nil
	case "WARN":
		return errs.Warn, nil
	case "RAISE":
		return errs.Raise, nil
	case "IMMEDIATE":
		return errs.Immediate, nil
	}
	return errs.Unset, fmt.Errorf("unknown error level %q", s)
}

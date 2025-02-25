// Copyright (c) 2021 Kells Kearney. All rights reserved.
//
// Use of this source code is governed by the MIT License that can be found
// in the LICENSE file.
//
package main

/*
 * This plugin saves raw SNMP traps to disk, in a fashion that can be replayed
 */

import (
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"

	pluginMeta "github.com/keruzu/trapmux/txPlugins"

	"github.com/rs/zerolog"
)

type trapCapture struct {
	dir        string
	fileExpr   string
	fileFormat string
	counter    int
	main_log *zerolog.Logger
}

const pluginName = "trap capture"

// currently only support gob format
func validateArguments(actionArgs map[string]string) error {
	validArgs := map[string]bool{"dir": true, "file_expr": true, "format": true}

	for key, _ := range actionArgs {
		if _, ok := validArgs[key]; !ok {
			return fmt.Errorf("Unrecognized option to %s plugin: %s", pluginName, key)
		}
	}

	return nil
}

func (a *trapCapture) Configure(pluginLog *zerolog.Logger, actionArgs map[string]string) error {
	a.main_log = pluginLog

	a.main_log.Info().Str("plugin", pluginName).Msg("Initialization of plugin")

	if err := validateArguments(actionArgs); err != nil {
		return err
	}

	a.dir = actionArgs["dir"]

	// If we don't get a file_expr, use a hard-coded name
	a.fileExpr = actionArgs["file_expr"]
	if a.fileExpr == "" {
		a.fileExpr = "captureFile"
	}

	a.fileFormat = actionArgs["format"]
	if a.fileFormat == "" {
		a.fileFormat = "gob"
	}
	a.main_log.Info().Str("file_expr", a.fileExpr).Str("dir", a.dir).Msg("Added capture destination")

	return nil
}

func (a *trapCapture) ProcessTrap(trap *pluginMeta.Trap) error {
	a.main_log.Info().Str("plugin", pluginName).Msg("Processing trap")
	var filename string
	var err error

	filename, err = makeCaptureFilename(a.dir, a.fileExpr, a.fileFormat, a.counter, trap)
	if err == nil {
		switch a.fileFormat {
		case "gob", "":
			err = saveCaptureGob(a.main_log, filename, trap)
		default:
			return fmt.Errorf("Unknown file format '%s'", a.fileFormat)
		}
	}
	a.counter++
	return err
}

func makeCaptureFilename(dir string, fileExpr string, format string, counter int, trap *pluginMeta.Trap) (string, error) {
	var filename string

	// FIXME: need to add templating capability
	filename = dir + "/" + fileExpr + fmt.Sprintf("-%v.%s", counter, format)
	return filename, nil
}

func saveCaptureGob(pluginLog *zerolog.Logger, filename string, trap *pluginMeta.Trap) error {
	fd, err := os.Create(filepath.Clean(filename))
	if err != nil {
		return err
	}

defer func() {
    if err := fd.Close(); err != nil {
                pluginLog.Error().Err(err).Str("capture_file", filename).Msg("Unable to load capture file")
    }
}()


	encoder := gob.NewEncoder(fd)
	return encoder.Encode(trap)
}

func (p trapCapture) SigUsr1() error {
	return nil
}

func (p trapCapture) SigUsr2() error {
	return nil
}

func (a trapCapture) Close() error {
	return nil
}

// Exported symbol which supports filter.go's FilterAction type
var ActionPlugin trapCapture

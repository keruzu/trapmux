// Copyright (c) 2021 Damien Stuart. All rights reserved.
//
// Use of this source code is governed by the MIT License that can be found
// in the LICENSE file.
//
package main

import (
	"fmt"
	"os"

	pluginLoader "github.com/keruzu/trapmux/api"
)

// On SIGHUP we reload the configuration.
//
func handleSIGHUP(sigCh chan os.Signal) {
	for {
		select {
		case <-sigCh:
			fmt.Printf("Got SIGHUP - Reloading configuration.\n")
			if err := getConfig(); err != nil {
				trapmuxLog.Info().Err(err).Msg("Error parsing configuration\nConfiguration was not changed")
			}
		}
	}
}

// Use SIGUSR2 to force a rotation of log files.
//
func handleSIGUSR2(sigCh chan os.Signal) {
	for {
		select {
		case <-sigCh:
			trapmuxLog.Info().Msg("Got SIGUSR2")
			for _, f := range teConfig.Filters {
				if f.actionType == actionPlugin {
				err :=	f.plugin.(pluginLoader.ActionPlugin).SigUsr2()
if err != nil {
				trapmuxLog.Warn().Err(err).Msg("Issue handling action")
}
				}
			}
		}
	}
}

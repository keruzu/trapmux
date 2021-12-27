// Copyright (c) 2021 Damien Stuart. All rights reserved.
//
// Use of this source code is governed by the MIT License that can be found
// in the LICENSE file.
//
package main

import (
	"flag"
        "io/ioutil"
        "path/filepath"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

        "github.com/creasty/defaults"
        "gopkg.in/yaml.v2"
	g "github.com/gosnmp/gosnmp"
)


/* ===========================================================
Notes on YAML configuration processing:
 * Variables that start with capital letters are processed (at least, for JSON)
 * Renaming of variables for the YAML file is done with the `yaml:` directives
 * Renamed variables *must* be in quotes to be recognized correctly (at least for underscores)
 * Default values are being applied with the creasty/defaults module
 * Non-basic types and classes can't be instantiated directly (eg g.SHA)
     * Configuration data structures have two sets of variables: text and usable
     * Per convention, the text versions start with uppercase, the usable ones start lowercase
 * Filter lines are very problematic for YAML
     * some characters (I'm looking at you ':' -- also regex) cause YAML to barf
     * using a more YAML-like structure will eat up huge chunks of configuration lines
        eg
         * * * * * ^1\.3\.6\.1\.4.1\.546\.1\.1 break

         vs

         - snmpversions: *
           source_ip: *
           agent_address: *
           ...
   ===========================================================
*/

type v3Params struct {
	MsgFlags        string `default:"NoAuthNoPriv" yaml:"msg_flags"`
	msgFlags        g.SnmpV3MsgFlags `default:"g.NoAuthNoPriv"`
	Username        string `default:"XXv3Username" yaml:"username"`
	AuthProto       string `default:"NoAuth" yaml:"auth_protocol"`
	authProto       g.SnmpV3AuthProtocol `default:"g.NoAuth"`
	AuthPassword    string `default:"XXv3authPass" yaml:"auth_password"`
	PrivacyProto    string `default:"NoPriv" yaml:"privacy_protocol"`
	privacyProto    g.SnmpV3PrivProtocol `default:"g.NoPriv"`
	PrivacyPassword string `default:"XXv3Pass" yaml:"privacy_password"`
}

type ipSet map[string]bool

type trapexConfig struct {
	teConfigured   bool
	runLogFile     string
	configFile     string

  General struct {
	Hostname     string `yaml:"hostname"`
	ListenAddr     string `default:"0.0.0.0" yaml:"listen_address"`
	ListenPort     string `default:"162" yaml:"listen_port"`

	IgnoreVersions []string `default:"[]" yaml:"ignore_versions"`
	ignoreVersions []g.SnmpVersion `default:"[]"`

	PrometheusIp   string `default:"0.0.0.0" yaml:"prometheus_ip"`
	PrometheusPort string `default:"80" yaml:"prometheus_port"`
	PrometheusEndpoint string `default:"metrics" yaml:"prometheus_endpoint"`
  }

  Logging struct {
	Level          string `default:"debug" yaml:"level"`
	LogMaxSize     int `default:"1024" yaml:"log_size_max"`
	LogMaxBackups  int `default:"7" yaml:"log_backups_max"`
	LogMaxAge      int `yaml:"log_age_max"`
	LogCompress    bool `default:"false" yaml:"compress"`
  }

	V3Params       v3Params `yaml:"snmpv3"`

	IpSets         []map[string][]string `default:"{}" yaml:"ipsets"`
	ipSets         map[string]ipSet `default:"{}"`

	RawFilters     []string `default:"[]" yaml:"filters"`
	filters        []trapexFilter
}

type trapexCommandLine struct {
	configFile string
	bindAddr   string
	listenPort string
	debugMode  bool
}

// Global vars
//
var teConfig *trapexConfig
var teCmdLine trapexCommandLine
var ipRe = regexp.MustCompile(`^(?:\d{1,3}\.){3}\d{1,3}$`)


func showUsage() {
	usageText := `
Usage: trapex [-h] [-c <config_file>] [-b <bind_ip>] [-p <listen_port>]
              [-d] [-v]
  -h  - Show this help message and exit.
  -c  - Override the location of the trapex configuration file.
  -b  - Override the bind IP address on which to listen for incoming traps.
  -p  - Override the UDP port on which to listen for incoming traps.
  -d  - Enable debug mode (note: produces very verbose runtime output).
  -v  - Print the version of trapex and exit.
`
	fmt.Println(usageText)
}

func processCommandLine() {
	flag.Usage = showUsage
	c := flag.String("c", "/etc/trapex.conf", "")
	b := flag.String("b", "", "")
	p := flag.String("p", "", "")
	d := flag.Bool("d", false, "")
	showVersion := flag.Bool("v", false, "")

	flag.Parse()

	if *showVersion {
		fmt.Printf("This is trapex version %s\n", myVersion)
		os.Exit(0)
	}

	teCmdLine.configFile = *c
	teCmdLine.bindAddr = *b
	teCmdLine.listenPort = *p
	teCmdLine.debugMode = *d
}

// loadConfig
// Load a YAML file with configuration, and create a new object
func loadConfig(config_file string, newConfig *trapexConfig) {
        defaults.Set(newConfig)

// FIXME: Is this required anymore?
	//newConfig.IpSets = make(map[string]ipSet)

        filename, _ := filepath.Abs(config_file)
        yamlFile, err := ioutil.ReadFile(filename)
	if err != nil {
// FIXME: What to do when file doesn't exist? Exit?
                fmt.Printf("%s\n", err)
	}
	err = yaml.Unmarshal(yamlFile, newConfig)
	if err != nil {
// FIXME: What to do when file doesn't parse? Exit?
                fmt.Printf("%s\n", err)
	}
}


func applyCliOverrides(newConfig *trapexConfig) {
        // Override the listen address:port if they were specified on the
        // command line.  If not and the listener values were not set in
        // the config file, fallback to defaults.
        if teCmdLine.bindAddr != "" {
                newConfig.General.ListenAddr = teCmdLine.bindAddr
        }
        if teCmdLine.listenPort != "" {
                newConfig.General.ListenPort = teCmdLine.listenPort
        }
        if teCmdLine.debugMode {
                newConfig.Logging.Level = "debug"
        }
        if newConfig.General.Hostname == "" {
                myName, err := os.Hostname()
                if err != nil {
                        newConfig.General.Hostname = "_undefined"
                } else {
                        newConfig.General.Hostname = myName
                }
        }
}


func getConfig() error {
	// If this is a reconfig close any current handles
	if teConfig != nil && teConfig.teConfigured {
		fmt.Printf("Reloading ")
	} else {
		fmt.Printf("Loading ")
	}
	fmt.Printf("configuration for trapex version %s from %s\n", myVersion, teCmdLine.configFile)

	var newConfig trapexConfig
        loadConfig(teCmdLine.configFile, &newConfig)

        applyCliOverrides(&newConfig)

        validateIgnoreVersions(&newConfig)

        // If this is a reconfigure, close the old handles here
        if teConfig != nil && teConfig.teConfigured {
                closeTrapexHandles()
        }
        // Set our global config pointer to this configuration
        newConfig.teConfigured = true
        teConfig = &newConfig

        return nil
}

func validateIgnoreVersions(newConfig *trapexConfig) error {
    var ignorev1, ignorev2c, ignorev3 bool = false, false, false
    for _, candidate := range newConfig.General.IgnoreVersions {
        switch strings.ToLower(candidate) {
            case "v1", "1":
                if ignorev1 != true {
                  newConfig.General.ignoreVersions = append(newConfig.General.ignoreVersions, g.Version1)
                  ignorev1 = true
                }
            case "v2c", "2c", "2":
                if ignorev2c != true {
                  newConfig.General.ignoreVersions = append(newConfig.General.ignoreVersions, g.Version2c)
                  ignorev2c = true
                }
            case "v3", "3":
                if ignorev3 != true {
                  newConfig.General.ignoreVersions = append(newConfig.General.ignoreVersions, g.Version3)
                  ignorev3 = true
                }
            default:
                  return fmt.Errorf("unsupported or invalid value (%s) for general:ignore_version", candidate)
            }
    }
    if len(newConfig.General.ignoreVersions) > 2 {
        return fmt.Errorf("All three SNMP versions are ignored -- there will be no traps to process")
    }
    return nil
}

/*
func getConfigOldStyle() error {
	cfSkipRe := regexp.MustCompile(`^\s*#|^\s*$`)
	ipRe := regexp.MustCompile(`^(?:\d{1,3}\.){3}\d{1,3}$`)

	var lineNumber uint = 0
	var processingIPSet bool = false
	var ipsName string

	scanner := bufio.NewScanner(cf)
	for scanner.Scan() {
		// Scan in the lines from the config file
		line := scanner.Text()
		lineNumber++
		if cfSkipRe.MatchString(line) {
			continue
		}

		// Split the line into fields
		f := strings.Fields(line)

		if processingIPSet {
			if f[0] == "}" {
				processingIPSet = false
				fmt.Printf("IP count: %v\n", len(newConfig.IpSets[ipsName]))
				continue
			}
			// Assume all fields are IP addresses
			for _, ip := range f {
				if ipRe.MatchString(ip) {
					newConfig.IpSets[ipsName][ip] = true
				} else {
					return fmt.Errorf("Invalid IP address (%s) in ipset: %s at line: %v", ip, ipsName, lineNumber)
				}
			}
		} else if f[0] == "ipset" {
			if len(f) < 3 || f[2] != "{" {
				return fmt.Errorf("Invalid format for ipset at line: %v: '%s'", lineNumber, line)
			}
			ipsName = f[1]
			newConfig.IpSets[ipsName] = make(map[string]bool)
			processingIPSet = true
			fmt.Printf(" -Add IPSet: %s - ", ipsName)
			continue
		} else if f[0] == "filter" {
			if err := processFilterLine(f[1:], &newConfig, lineNumber); err != nil {
				return err
			}
		} else {
			if err := processConfigLine(f, &newConfig, lineNumber); err != nil {
				return err
			}
		}
	}

		newConfig.v3Params.username = defV3user
	}
	if newConfig.v3Params.authProto == 0 {
		newConfig.v3Params.authProto = defV3authProtocol
	}
	if newConfig.v3Params.authPassword == "" {
		newConfig.v3Params.authPassword = defV3authPassword
	}
	if newConfig.v3Params.privacyProto == 0 {
		newConfig.v3Params.privacyProto = defV3privacyProtocol
	}
	if newConfig.v3Params.privacyPassword == "" {
		newConfig.v3Params.privacyPassword = defV3privacyPassword
	}
	// Sanity-check the v3 params
	//
	if (newConfig.v3Params.msgFlags&g.AuthPriv) == 1 && newConfig.v3Params.authProto < 2 {
		return fmt.Errorf("v3 config error: no auth protocol set when msgFlags specifies an Auth mode")
	}
	if newConfig.v3Params.msgFlags == g.AuthPriv && newConfig.v3Params.privacyProto < 2 {
		return fmt.Errorf("v3 config error: no privacy protocol mode set when msgFlags specifies an AuthPriv mode")
	}
}
*/

// processFilterLine parsed a "filter" line from the config file and sets
// the appropriate values in the corresponding trapexFilter struct.
//
func processFilterLine(f []string, newConfig *trapexConfig, lineNumber uint) error {
	var err error
	if len(f) < 7 {
		return fmt.Errorf("not enough fields in filter line(%v): %s", lineNumber, "filter "+strings.Join(f, " "))
	}

	// Process the filter criteria
	//
	filter := trapexFilter{}
	if strings.HasPrefix(strings.Join(f, " "), "* * * * * *") {
		filter.matchAll = true
	} else {
		fObj := filterObj{}
		// Construct the filter criteria
		for i, fi := range f[:6] {
			if fi == "*" {
				continue
			}
			fObj.filterItem = i
			if i == 0 {
				switch strings.ToLower(fi) {
				case "v1", "1":
					fObj.filterValue = g.Version1
				case "v2c", "2c", "2":
					fObj.filterValue = g.Version2c
				case "v3", "3":
					fObj.filterValue = g.Version3
				default:
					return fmt.Errorf("unsupported or invalid SNMP version (%s) on line %v for filter", fi, lineNumber)
				}
				fObj.filterType = parseTypeInt // Just because we should set this to something.
			} else if i == 1 || i == 2 { // Either of the first 2 is an IP address type
				if strings.HasPrefix(fi, "ipset:") { // If starts with a "ipset:"" it's an IP set
					fObj.filterType = parseTypeIPSet
/*
					if _, ok := newConfig.IpSets[fi[6:]]; ok {
						fObj.filterValue = fi[6:]
					} else {
						return fmt.Errorf("Invalid ipset name specified on line %v: %s", lineNumber, fi)
					}
*/
				} else if strings.HasPrefix(fi, "/") { // If starts with a "/", it's a regex
					fObj.filterType = parseTypeRegex
					fObj.filterValue, err = regexp.Compile(fi[1:])
					if err != nil {
						return fmt.Errorf("unable to compile regexp for IP on line %v: %s: %s", lineNumber, fi, err)
					}
				} else if strings.Contains(fi, "/") {
					fObj.filterType = parseTypeCIDR
					fObj.filterValue, err = newNetwork(fi)
					if err != nil {
						return fmt.Errorf("invalid IP/CIDR at line %v: %s", lineNumber, fi)
					}
				} else {
					fObj.filterType = parseTypeString
					fObj.filterValue = fi
				}
			} else if i > 2 && i < 5 { // Generic and Specific type
				val, e := strconv.Atoi(fi)
				if e != nil {
					return fmt.Errorf("invalid integer value at line %v: %s: %s", lineNumber, fi, e)
				}
				fObj.filterType = parseTypeInt
				fObj.filterValue = val
			} else { // The enterprise OID
				fObj.filterType = parseTypeRegex
				fObj.filterValue, err = regexp.Compile(fi)
				if err != nil {
					return fmt.Errorf("unable to compile regexp at line %v for OID: %s: %s", lineNumber, fi, err)
				}
			}
			filter.filterItems = append(filter.filterItems, fObj)
		}
	}
	// Process the filter action
	//
	var actionArg string
	var breakAfter bool
	if len(f) > 8 && f[8] == "break" {
		breakAfter = true
	} else {
		breakAfter = false
	}

	var action = f[6]

	if len(f) > 7 {
		actionArg = f[7]
	}

	switch action {
	case "break", "drop":
		filter.actionType = actionBreak
	case "nat":
		filter.actionType = actionNat
		if actionArg == "" {
			return fmt.Errorf("missing nat argument at line %v", lineNumber)
		}
		filter.actionArg = actionArg
	case "forward":
		if breakAfter {
			filter.actionType = actionForwardBreak
		} else {
			filter.actionType = actionForward
		}
		forwarder := trapForwarder{}
		if err := forwarder.initAction(actionArg); err != nil {
			return err
		}
		filter.action = &forwarder
	case "log":
		if breakAfter {
			filter.actionType = actionLogBreak
		} else {
			filter.actionType = actionLog
		}
		logger := trapLogger{}
		if err := logger.initAction(actionArg, newConfig); err != nil {
			return err
		}
		filter.action = &logger
	case "csv":
		if breakAfter {
			filter.actionType = actionCsvBreak
		} else {
			filter.actionType = actionCsv
		}
		csvLogger := trapCsvLogger{}
		if err := csvLogger.initAction(actionArg, newConfig); err != nil {
			return err
		}
		filter.action = &csvLogger
	default:
		return fmt.Errorf("unknown action: %s at line %v", action, lineNumber)
	}

	newConfig.filters = append(newConfig.filters, filter)

	return nil
}

func processConfigLine(f []string, newConfig *trapexConfig, lineNumber uint) error {
	flen := len(f)
	switch f[0] {
	case "debug":
		newConfig.Logging.Level = "debug"
	case "hostname":
		if flen < 2 {
			return fmt.Errorf("missing value for hostname at line %v", lineNumber)
		}
		newConfig.General.Hostname = f[1]
	case "listenAddress":
		if flen < 2 {
			return fmt.Errorf("missing value for listenAddr at line %v", lineNumber)
		}
		newConfig.General.ListenAddr = f[1]
	case "listenPort":
		if flen < 2 {
			return fmt.Errorf("missing value for listenPort at line %v", lineNumber)
		}
		p, err := strconv.ParseUint(f[1], 10, 16)
		if err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("invalid listenPort value: %s at line %v", err, lineNumber)
		}
		newConfig.General.ListenPort = f[1]
	case "ignoreVersions":
		if flen < 2 {
			return fmt.Errorf("missing value for ignoreVersions at line %v", lineNumber)
		}
		// split on commas (if any)
		for _, v := range strings.Split(f[1], ",") {
			switch strings.ToLower(v) {
			case "v1", "1":
				newConfig.General.ignoreVersions = append(newConfig.General.ignoreVersions, g.Version1)
			case "v2c", "2c", "2":
				newConfig.General.ignoreVersions = append(newConfig.General.ignoreVersions, g.Version2c)
			case "v3", "3":
				newConfig.General.ignoreVersions = append(newConfig.General.ignoreVersions, g.Version3)
			default:
				return fmt.Errorf("unsupported or invalid value (%s) for ignoreVersion at line %v", v, lineNumber)
			}
		}
		if len(newConfig.General.IgnoreVersions) > 2 {
			return fmt.Errorf("All 3 SNMP versions are ignored at line %v. There will be no traps to process", lineNumber)
		}
	case "v3msgFlags":
		if flen < 2 {
			return fmt.Errorf("missing value for v3msgFlags at line %v", lineNumber)
		}
		switch f[1] {
		case "NoAuthNoPriv":
			newConfig.V3Params.msgFlags = g.NoAuthNoPriv
		case "AuthNoPriv":
			newConfig.V3Params.msgFlags = g.AuthNoPriv
		case "AuthPriv":
			newConfig.V3Params.msgFlags = g.AuthPriv
		default:
			return fmt.Errorf("unsupported or invalid value (%s) for v3msgFlags at line %v", f[1], lineNumber)
		}
	case "v3user":
		if flen < 2 {
			return fmt.Errorf("missing value for v3user at line %v", lineNumber)
		}
		newConfig.V3Params.Username = f[1]
	case "v3authProtocol":
		if flen < 2 {
			return fmt.Errorf("missing value for v3authProtocol at line %v", lineNumber)
		}
		switch f[1] {
                // AES is *NOT* supported
                //  cannot use gosnmp.AES (type gosnmp.SnmpV3PrivProtocol) as type gosnmp.SnmpV3AuthProtocol in assignment
		//case "AES":
			//newConfig.V3Params.authProto = g.AES
		case "SHA":
			newConfig.V3Params.authProto = g.SHA
		case "MD5":
			newConfig.V3Params.authProto = g.MD5
		default:
			return fmt.Errorf("invalid value for v3authProtocol at line %v", lineNumber)
		}
	case "v3authPassword":
		if flen < 2 {
			return fmt.Errorf("missing value for v3authPassword at line %v", lineNumber)
		}
		newConfig.V3Params.AuthPassword = f[1]
	case "v3privacyProtocol":
		if flen < 2 {
			return fmt.Errorf("missing value for v3privacyProtocol at line %v", lineNumber)
		}
		switch f[1] {
		case "AES":
			newConfig.V3Params.privacyProto = g.AES
		case "DES":
			newConfig.V3Params.privacyProto = g.DES
		default:
			return fmt.Errorf("invalid value for v3privacyProtocol at line %v", lineNumber)
		}
	case "v3privacyPassword":
		if flen < 2 {
			return fmt.Errorf("missing value for v3privacyPassword at line %v", lineNumber)
		}
		newConfig.V3Params.PrivacyPassword = f[1]
	case "logfileMaxSize":
		if flen < 2 {
			return fmt.Errorf("missing value for logfileMaxSize at line %v", lineNumber)
		}
		p, err := strconv.Atoi(f[1])
		if err != nil || p < 1 {
			return fmt.Errorf("invalid logfileMaxSize value: %s at line %v", err, lineNumber)
		}
		newConfig.Logging.LogMaxSize = p
	case "logfileMaxBackups":
		if flen < 2 {
			return fmt.Errorf("missing value for logfileMaxBackups at line %v", lineNumber)
		}
		p, err := strconv.Atoi(f[1])
		if err != nil || p < 1 {
			return fmt.Errorf("invalid logfileMaxBackups value: %s at line %v", err, lineNumber)
		}
		newConfig.Logging.LogMaxBackups = p
	case "compressRotatedLogs":
		newConfig.Logging.LogCompress = true
	case "prometheus_ip":
		if flen < 2 {
			return fmt.Errorf("missing value for prometheus_ip at line %v", lineNumber)
		}
		newConfig.General.PrometheusIp = f[1]
	case "prometheus_port":
		if flen < 2 {
			return fmt.Errorf("missing value for prometheus_port at line %v", lineNumber)
		}
		newConfig.General.PrometheusPort = f[1]
	case "prometheus_endpoint":
		if flen < 2 {
			return fmt.Errorf("missing value for prometheus_endpoint at line %v", lineNumber)
		}
		newConfig.General.PrometheusEndpoint = f[1]
	default:
		return fmt.Errorf("Unknown/unsuppported configuration option: %s at line %v", f[0], lineNumber)
	}
	return nil
}

func closeTrapexHandles() {
	for _, f := range teConfig.filters {
		if f.actionType == actionForward || f.actionType == actionForwardBreak {
			f.action.(*trapForwarder).close()
		}
		if f.actionType == actionLog || f.actionType == actionLogBreak {
			f.action.(*trapLogger).close()
		}
		if f.actionType == actionCsv || f.actionType == actionCsvBreak {
			f.action.(*trapCsvLogger).close()
		}
	}
}

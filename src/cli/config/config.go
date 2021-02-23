package main

import (
    "os"
    "os/exec"
    "io/ioutil"
	"fmt"
	"strings"
    "path"
	"goharvest2/share/config"
	"goharvest2/share/dialog"
	"goharvest2/share/tree"
	"goharvest2/share/tree/node"
    "goharvest2/poller/api/zapi"
)

var USAGE = `
Harvest 2.0 - Config utility

Configure a new poller or exporter

Usage: harvest2 config ["poller" | "exporter"]

Poller:
  A poller is an Harvest instance for monitoring one single
  storage system. This utility helps you to create a poller 
  for a NetApp System (Cdot or 7Mode). For a custom poller,
  just edit your config.yaml manually.

Exporter:
  An exporter is an interface that forwards data to a database.
  The same exporter can be used by more than one pollers, i.e.
  you need to define only one exporter for each of your DBs.
  This utility helps you to create exporters for three DBs:
  Prometheus, InfluxDB and Graphite
`

var PATH = "/opt/harvest2"

var HARVEST_USER = "harvest2-user"
var HARVEST_ROLE = "harvest2-role"

var DIALOG *dialog.Dialog

func print_usage() {
	fmt.Println(USAGE)
}

func exitError(msg string, err error) {
    DIALOG.Close()
    fmt.Printf("Error (%s): %v\n", msg, err)
    //os.Exit(1)
}

func main() {

	var item string
    var err error
	var conf, pollers, exporters *node.Node

	if len(os.Args) > 1 {
		item = strings.ReplaceAll(os.Args[1], "-", "")
	}

	if item == "help" {
		print_usage()
		os.Exit(0)
	}

    DIALOG = dialog.New()
    fmt.Println(DIALOG.Info())

	if item == "welcome" {

        DIALOG.SetTitle("harvest 2.0 - welcome")
		DIALOG.Message("Your installation is complete. Welcome to Harvest 2.0!")

		if DIALOG.YesNo("Do you want to quickly configure Harvest?") {
			item = ""
		} else {
		    item = "exit"
        }
	}

	DIALOG.SetTitle("harvest 2.0 - config")

    if item == "exit" {
        DIALOG.Message("Bye! If you want my help next time, run: \"harvest config\"")
    }

    if conf, err = config.LoadConfig(PATH, "config.yaml"); err != nil {
        conf = node.NewS("")
    }

    if pollers = conf.GetChildS("Pollers"); pollers == nil {
        pollers = conf.NewChildS("Pollers", "")
    }

    if exporters = conf.GetChildS("Exporters"); exporters == nil {
        exporters = conf.NewChildS("Exporters", "")
    }

    for {

        if item == "" {
            item, err = DIALOG.Menu("Add new:", "poller", "exporter", "safe and exit")
            if err != nil {
                exitError("menu add new", err)
            }
        }
    
        if item == "poller" {
            if new_poller := add_poller(); new_poller != nil {
                if pollers.GetChildS(new_poller.GetNameS()) == nil {
                    pollers.AddChild(new_poller)
                } else if DIALOG.YesNo("poller [" + new_poller.GetNameS() + "] already exists, overwrite?") {
                    pollers.AddChild(new_poller)
                }
            }
        }
        
        if item == "exporter" {
            if new_exporter := add_exporter(); new_exporter != nil {
                if exporters.GetChildS(new_exporter.GetNameS()) == nil {
                    exporters.AddChild(new_exporter)
                } else if DIALOG.YesNo("exporter [" + new_exporter.GetNameS() + "] already exists, overwrite?") {
                    exporters.AddChild(new_exporter)
                }
            }
        }

        if item == "safe and exit" {
            break
        }

        item = ""
    }

    fp := path.Join(PATH, "config.yaml")
    if err = tree.ExportYaml(conf, fp); err != nil {
        exitError("export yaml", err)
    }
    DIALOG.Message(fmt.Sprintf("Saved results to:\n[%s]", fp))
    DIALOG.Close()

    //conf.Print(0)
}

func add_poller() *node.Node {

    poller := node.NewS("")

    // ask for datacenter & address

    datacenter, err := DIALOG.Input("Datacenter name:")
    if err != nil {
        exitError("input datacenter", err)
    }
    poller.NewChildS("datacenter", datacenter)

    addr, err := DIALOG.Input("Enter address (IPv4, IPv6, hostname or URL)")
    if err != nil {
        exitError("input addr", err)
    }
    poller.NewChildS("url", addr)

    // ask for authentication method
    auth, err := DIALOG.Menu("Choose authentication method", "client certificate (recommended)", "password")
    if err != nil {
        exitError("menu auth", err)
    }

    create_cert := false

    if auth == "client certificate (recommended)" {
        if DIALOG.YesNo("Create client certificate and key pair?") {
            if exec.Command("which", "openssl").Run() != nil {
                DIALOG.Message("You don't have openssl installed, please install and try again")
                return nil
            }
            create_cert = true
            DIALOG.Message("This requires one-time admin password to create \na read-only user and install certificate on your system")
        } else {
            msg := fmt.Sprintf("Copy your cert/key pair to [%s/cert/] as [<SYSTEM_NAME>.key] and [<SYSTEM_NAME>.pem] to continue", PATH)
            DIALOG.Message(msg)
            poller.NewChildS("auth_style", "certificate_auth")
        }
    }

    if auth == "password" || create_cert {
        poller.NewChildS("auth_style", "password")
        username, _ := DIALOG.Input("username: ")
        password, _ := DIALOG.Password("password: ")
        poller.NewChildS("username", username)
        poller.NewChildS("password", password)
    }

    // connect and get system info
    DIALOG.Message("Connecting to system...")
    
    client, err := zapi.New(poller)
    if err != nil {
        exitError("client", err)
    }

    system, err := client.GetSystem()
    if err != nil {
        exitError("system", err)
        poller.Print(0)
        os.Exit(1)
    }

    poller.SetNameS(system.Name)
    DIALOG.Message("Connected to:\n" + system.String())


    if create_cert {

        cert_path := path.Join(PATH, "cert", system.Name + ".pem")
        key_path := path.Join(PATH, "cert", system.Name + ".key")

        cmd := exec.Command(
            "openssl", 
            "req", 
            "-x509", 
            "-nodes", 
            "-days", 
            "1095", 
            "-newkey", 
            "rsa:2048", 
            "-keyout", 
            key_path, 
            "-out", 
            cert_path, 
            "-subj", 
            "/CN="+HARVEST_USER,
        )

        if err := cmd.Run(); err != nil {
            exitError("openssl", err)
        }

        DIALOG.Message(fmt.Sprintf("Generated certificate/key pair:\n  - %s\n  - %s\n", cert_path, key_path))

        req := node.NewXmlS("security-login-role-create")
        req.NewChildS("access-level", "readonly")
        req.NewChildS("command-directory-name", "DEFAULT")
        req.NewChildS("role-name", HARVEST_ROLE)
        req.NewChildS("vserver", system.Name)

        if _, err := client.InvokeRequest(req); err != nil {
            exitError("create role", err)
        }

        req = node.NewXmlS("security-login-create")
        req.NewChildS("application", "ontapi")
        req.NewChildS("authentication-method", "cert")
        req.NewChildS("comment", "readonly user for harvest2")
        req.NewChildS("role-name", HARVEST_ROLE)
        req.NewChildS("user-name", HARVEST_USER)
        req.NewChildS("vserver", system.Name)

        if _, err := client.InvokeRequest(req); err != nil {
            exitError("create user", err)
        }

        DIALOG.Message(fmt.Sprintf("Created read-only user [%s] and role [%s]", HARVEST_USER, HARVEST_ROLE))

        cert_content, err := ioutil.ReadFile(cert_path)
        if err != nil {
            exitError("cert content", err)
        }

        req = node.NewXmlS("security-certificate-install")
        req.NewChildS("cert-name", HARVEST_USER)
        req.NewChildS("certificate", string(cert_content))
        req.NewChildS("type", "client_ca")
        req.NewChildS("vserver", system.Name)
        
        if _, err := client.InvokeRequest(req); err != nil {
            exitError("install cert", err)
        }
    
        DIALOG.Message("Certificate installed on system.")

        poller.PopChildS("auth_style")
        poller.PopChildS("username")
        poller.PopChildS("password")
        poller.NewChildS("auth_style", "certificate_auth")
        poller.NewChildS("ssl_cert", cert_path)
	    poller.NewChildS("ssl_key", key_path)
    }
    

    collectors := poller.NewChildS("collectors", "")
    collectors.NewChildS("", "Zapi")

	return poller
}

func add_exporter() *node.Node {

    exporter := node.NewS("")

    item, err := DIALOG.Menu("Choose exporter type:", "prometheus", "influxdb", "graphite")
    if err != nil {
        exitError("exporter type", err)
    }
    exporter.NewChildS("exporter", item)

    name, err := DIALOG.Input("Choose name for exporter instance:")
    if err != nil {
        exitError("input exporter name", err)
    }
    exporter.SetNameS(name)


    port, err := DIALOG.Input("Port of the HTTP service:")
    if err != nil {
        exitError("input exporter port", err)
    }
    exporter.NewChildS("port", port)

    if DIALOG.YesNo("Make HTTP serve publicly on your network?\n(Choose no to serve it only on localhst)") {
        exporter.NewChildS("url", "0.0.0.0")
    } else {
        exporter.NewChildS("url", "localhost")
    }

    exporter.NewChildS("master", "True")

	return exporter
}
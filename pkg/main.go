package main

import (
	"os"

	"github.com/domotz/domotz-datasource/pkg/plugin"
	"github.com/grafana/grafana-plugin-sdk-go/backend/datasource"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"
)

// pluginID must match the id in src/plugin.json.
const pluginID = "domotz-datasource"

func main() {
	// datasource.Manage blocks until Grafana shuts the process down. It owns
	// the lifecycle of one DomotzDatasource instance per configured data
	// source, disposing and recreating an instance whenever its settings change.
	if err := datasource.Manage(pluginID, plugin.NewDatasource, datasource.ManageOpts{}); err != nil {
		log.DefaultLogger.Error(err.Error())
		os.Exit(1)
	}
}

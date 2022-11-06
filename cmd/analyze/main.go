package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/digitalocean/apps-self-check/pkg/storer"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var ctx context.Context

var maxInterval = &cobra.Command{
	Use: "analyze",
	Run: func(cmd *cobra.Command, args []string) {
		storer, err := storer.NewMySQLStorer(ctx, os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_CA_CERT"))
		if err != nil {
			log.Fatal().Err(err).Msg("creating storer")
		}
		defer storer.Close()
		start, err := cmd.Flags().GetString("start")
		if err != nil {
			log.Fatal().Err(err).Msg("parsing start flag")
		}
		startTS, err := time.ParseInLocation(time.RFC3339Nano, start, time.UTC)
		if err != nil {
			log.Fatal().Err(err).Msg("parsing start timestamp")
		}
		end, err := cmd.Flags().GetString("end")
		if err != nil {
			log.Fatal().Err(err).Msg("parsing end flag")
		}
		endTS, err := time.ParseInLocation(time.RFC3339Nano, end, time.UTC)
		if err != nil {
			log.Fatal().Err(err).Msg("parsing end timestamp")
		}
		err = storer.AnalyzeLongestGapPerApp(ctx, startTS, endTS, func(appID string, interval time.Duration, maxIntervalTS time.Time) {
			fmt.Printf("%s,%f,%v\n", appID, interval.Seconds(), maxIntervalTS)
		})
		if err != nil {
			log.Fatal().Err(err).Msg("analyzing log gaps")
		}
	},
}

func init() {
	maxInterval.Flags().String("start", "", "start timestamp")
	maxInterval.Flags().String("end", "", "start timestamp")
}

func main() {
	var stop func()
	ctx, stop = signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := maxInterval.Execute(); err != nil {
		log.Fatal().Err(err).Msg("creating storer")
	}
}

package restapi

import (
	"compress/flate"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lodthe/clickhouse-playground/internal/metrics"
	"github.com/lodthe/clickhouse-playground/internal/queryrun"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/rs/zerolog"
)

type RouterOpts struct {
	Logger     zerolog.Logger
	Runner     QueryRunner
	Preparer   ImagePreparer
	TagStorage TagStorage
	RunRepo    queryrun.Repository

	Timeout       time.Duration
	CacheDisabled bool

	MaxQueryLength  uint64
	MaxOutputLength uint64
}

func NewRouter(opts RouterOpts) http.Handler {
	r := chi.NewRouter()

	r.Use(metricsMiddleware)

	r.Use(middleware.RequestID)
	r.Use(middleware.RequestLogger(&middleware.DefaultLogFormatter{
		Logger:  &opts.Logger,
		NoColor: true,
	}))
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(flate.DefaultCompression, "application/json"))

	r.Use(middleware.Timeout(opts.Timeout))
	if opts.CacheDisabled {
		r.Use(middleware.NoCache)
	}

	r.Use(cors.Handler(cors.Options{
		// AllowedOrigins:   []string{"https://foo.com"}, // Use this to allow specific origin hosts
		AllowedOrigins:   []string{"https://*", "http://*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Route("/api", func(r chi.Router) {
		newQueryHandler(opts.Runner, opts.RunRepo, opts.TagStorage, opts.MaxQueryLength, opts.MaxOutputLength).handle(r)
		newImageTagHandler(opts.TagStorage).handle(r)
		newImageBuildHandler(opts.Preparer, opts.TagStorage).handle(r)
	})

	return r
}

func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		rctx := chi.RouteContext(r.Context())
		routePattern := strings.Join(rctx.RoutePatterns, "")

		status := fmt.Sprintf("%d %s", ww.Status(), http.StatusText(ww.Status()))
		metrics.RestAPI.NewRequest(r.Method, routePattern, status, time.Since(start))
	})
}

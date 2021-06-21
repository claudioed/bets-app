package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/labstack/echo"
	"github.com/labstack/echo/middleware"
	"github.com/motemen/go-loghttp"
	"github.com/rs/zerolog"

	"io/ioutil"
	"net/http"
	"os"
)

var log *zerolog.Logger
var client *http.Client
var incomingHeaders = []string{
	"Authorization",
	"x-version",

	// open tracing
	"x-request-id",
	"x-b3-traceid",
	"x-b3-spanid",
	"x-b3-parentspanid",
	"x-b3-sampled",
	"x-b3-flags",
	"x-ot-span-context",
}

func init() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	output := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
	logger := zerolog.New(output).With().Timestamp().Caller().Logger()
	log = &logger
	transport := &loghttp.Transport{
		LogRequest: func(req *http.Request) {
			log.Debug().
				Interface("headers", req.Header).
				Msg("calling " + req.Method + " " + req.URL.String())
		},

		LogResponse: func(res *http.Response) {
			req := res.Request
			log.Debug().
				Str("status", res.Status).
				Interface("headers", res.Header).
				Msg("call " + req.Method + " " + req.URL.String() + " answered")
		},
	}
	client = &http.Client{Transport: transport}
}

func main() {
	start := time.Now()
	e := echo.New()
	e.Logger.SetOutput(ioutil.Discard)
	// Middleware
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			req := c.Request()
			res := c.Response()
			start := time.Now()
			log.Debug().
				Interface("headers", req.Header).
				Msg(">>> " + req.Method + " " + req.RequestURI)
			if err = next(c); err != nil {
				c.Error(err)
			}
			log.Debug().
				Str("latency", time.Now().Sub(start).String()).
				Int("status", res.Status).
				Interface("headers", res.Header()).
				Msg("<<< " + req.Method + " " + req.RequestURI)
			return
		}
	})
	e.Use(middleware.Recover())
	//CORS
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{echo.GET, echo.HEAD, echo.PUT, echo.PATCH, echo.POST, echo.DELETE},
	}))

	e.Static("/static", "assets")

	// Server
	e.POST("/api/bets", CreateBet)
	e.GET("/health", Health)
	elapsed := time.Now().Sub(start)
	log.Debug().Msg("Bets app initialized in " + elapsed.String())
	e.Logger.Fatal(e.Start(":9999"))
}

func Health(c echo.Context) error {
	return c.JSON(200, &HealthData{Status: "UP"})
}

type HealthData struct {
	Status string `json:"status,omitempty"`
}

func CreateBet(c echo.Context) error {

	defer c.Request().Body.Close()
	bet := &Bet{}

	if c.Request().Header.Get("Content-Type") != "application/json" {
		return echo.NewHTTPError(http.StatusUnsupportedMediaType)
	}

	if err := json.NewDecoder(c.Request().Body).Decode(bet); err != nil {
		log.Error().Err(err).Msg("Failed reading the request body")
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error)
	}

	match, matchStatus, matchErr := match(c)
	player, playerStatus, playerErr := player(c)
	champ, champStatus, champErr := championship(c)

	if hasError(matchErr, playerErr, champErr) {
		return c.JSON(http.StatusServiceUnavailable, &Error{Errors: map[string]int{
			"players":       playerStatus,
			"matches":       matchStatus,
			"championships": champStatus,
		}})
	}

	b := &Bet{
		HomeTeamScore: strconv.Itoa(2),
		AwayTeamScore: strconv.Itoa(3),
		Championship:  champ,
		Match:         match.String(),
		Email:         player,
	}
	return c.JSON(http.StatusCreated, b)
}

func hasError(errs ...error) bool {
	r := false
	for _, err := range errs {
		if err != nil {
			r = true
		}
	}
	return r
}

func match(ctx echo.Context) (*Match, int, error) {
	req, _ := http.NewRequest("GET", os.Getenv("MATCH_SVC"), nil)

	forwardHeaders(ctx, req)
	res, err := client.Do(req)
	if err != nil {
		log.Error().Err(err).Msg("failed to call matches")
		return nil, 0, err
	}
	status := res.StatusCode
	if !is2xx(status) {
		return nil, status, errors.New(res.Status)
	}
	data := &Match{}
	if jsonErr := json.NewDecoder(res.Body).Decode(data); jsonErr != nil {
		log.Error().Err(jsonErr).Msg("failed to read matches response body")
		return nil, 0, jsonErr
	}

	return data, status, nil
}

func forwardHeaders(ctx echo.Context, r *http.Request) {

	for _, th := range incomingHeaders {
		h := ctx.Request().Header.Get(th)
		if h != "" {
			r.Header.Set(th, h)
		}
	}
}

func championship(ctx echo.Context) (string, int, error) {
	req, _ := http.NewRequest("GET", os.Getenv("CHAMPIONSHIP_SVC"), nil)

	forwardHeaders(ctx, req)
	res, err := client.Do(req)
	if err != nil {
		log.Error().Err(err).Msg("failed to call championships")
		return "", 0, err
	}
	status := res.StatusCode
	if !is2xx(status) {
		return "", status, errors.New(res.Status)
	}
	body, readErr := ioutil.ReadAll(res.Body)
	if readErr != nil {
		log.Error().Err(err).Msg("failed to read matches response body")
		return "", status, readErr
	}

	var data map[string]string

	if jsonErr := json.Unmarshal(body, &data); jsonErr != nil {
		log.Error().Err(err).Msg("failed to read matches response body")
		return "", status, jsonErr
	}
	return data["title"], status, nil
}

func player(ctx echo.Context) (string, int, error) {
	req, _ := http.NewRequest("GET", os.Getenv("PLAYER_SVC"), nil)

	forwardHeaders(ctx, req)
	res, err := client.Do(req)
	if err != nil {
		log.Error().Err(err).Msg("failed to call players")
		return "", 0, err
	}
	status := res.StatusCode
	if !is2xx(status) {
		return "", status, errors.New(res.Status)
	}
	body, readErr := ioutil.ReadAll(res.Body)
	if readErr != nil {
		log.Error().Err(err).Msg("failed to read players response body")
		return "", status, readErr
	}

	var data map[string]string

	if jsonErr := json.Unmarshal(body, &data); jsonErr != nil {
		log.Error().Err(err).Msg("failed to read players response body")
		return "", status, jsonErr
	}
	return data["email"], status, nil
}

func is2xx(status int) bool {
	return status >= 200 && status < 300
}

type Bet struct {
	HomeTeamScore string `json:"homeTeamScore,omitempty"`
	AwayTeamScore string `json:"awayTeamScore,omitempty"`
	Championship  string `json:"championship,omitempty"`
	Match         string `json:"match,omitempty"`
	Email         string `json:"email,omitempty"`
}

type Error struct {
	Errors map[string]int `json:"errors,omitempty"`
}

type Match struct {
	HomeTeam     string `json:"homeTeam,omitempty"`
	AwayTeam     string `json:"awayTeam,omitempty"`
	Championship string `json:"championship,omitempty"`
}

func (m *Match) String() string {
	h := m.HomeTeam
	a := m.AwayTeam
	return fmt.Sprintf("%s %dx%d %s", h, 2, 3, a)
}

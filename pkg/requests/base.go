package requests

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/stripe/stripe-cli/pkg/ansi"
	"github.com/stripe/stripe-cli/pkg/profile"
	"github.com/stripe/stripe-cli/pkg/stripe"

	"github.com/spf13/cobra"
)

// RequestParameters captures the structure of the parameters that can be sent to Stripe
type RequestParameters struct {
	data          []string
	expand        []string
	startingAfter string
	endingBefore  string
	idempotency   string
	limit         string
	version       string
	stripeAccount string
}

// Base does stuff
type Base struct {
	Cmd *cobra.Command

	Method  string
	Profile profile.Profile

	// SuppressOutput is used by `trigger` to hide output
	SuppressOutput bool

	APIBaseURL string

	autoConfirm bool
	showHeaders bool
}

var parameters RequestParameters

var confirmationCommands = map[string]bool{"DELETE": true}

// RunRequestsCmd is the interface exposed for the CLI to run network requests through
func (rb *Base) RunRequestsCmd(cmd *cobra.Command, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("this command only supports one argument. Run with the --help flag to see usage and examples")
	}

	confirmed, err := rb.confirmCommand()
	if err != nil {
		return err
	} else if !confirmed {
		fmt.Println("Exiting without execution. User did not confirm the command.")
		return nil
	}

	secretKey, err := rb.Profile.GetSecretKey()
	if err != nil {
		return err
	}

	path := normalizePath(args[0])

	_, err = rb.MakeRequest(secretKey, path, &parameters)

	return err
}

// InitFlags initialize shared flags for all requests commands
func (rb *Base) InitFlags() {
	rb.Cmd.Flags().StringArrayVarP(&parameters.data, "data", "d", []string{}, "Data to pass for the API request")
	rb.Cmd.Flags().StringArrayVarP(&parameters.expand, "expand", "e", []string{}, "Response attributes to expand inline. Available on all API requests, see the documentation for specific objects that support expansion")
	rb.Cmd.Flags().StringVarP(&parameters.idempotency, "idempotency", "i", "", "Sets the idempotency key for your request, preventing replaying the same requests within a 24 hour period")
	rb.Cmd.Flags().StringVarP(&parameters.version, "api-version", "v", "", "Set the Stripe API version to use for your request")
	rb.Cmd.Flags().StringVar(&parameters.stripeAccount, "stripe-account", "", "Set a header identifying the connected account for which the request is being made")
	rb.Cmd.Flags().BoolVarP(&rb.showHeaders, "show-headers", "s", false, "Show headers on responses to GET, POST, and DELETE requests")
	rb.Cmd.Flags().BoolVarP(&rb.autoConfirm, "confirm", "c", false, "Automatically confirm the command being entered. WARNING: This will result in NOT being prompted for confirmation for certain commands")

	// Conditionally add flags for GET requests. I'm doing it here to keep `limit`, `start_after` and `ending_before` unexported
	if rb.Method == "GET" {
		rb.Cmd.Flags().StringVarP(&parameters.limit, "limit", "l", "", "A limit on the number of objects to be returned, between 1 and 100 (default is 10)")
		rb.Cmd.Flags().StringVarP(&parameters.startingAfter, "starting-after", "a", "", "Retrieve the next page in the list. This is a cursor for pagination and should be an object ID")
		rb.Cmd.Flags().StringVarP(&parameters.endingBefore, "ending-before", "b", "", "Retrieve the previous page in the list. This is a cursor for pagination and should be an object ID")
	}

	// Hidden configuration flags, useful for dev/debugging
	rb.Cmd.Flags().StringVar(&rb.APIBaseURL, "api-base", stripe.DefaultAPIBaseURL, "Sets the API base URL")
	rb.Cmd.Flags().MarkHidden("api-base") // #nosec G104
}

// MakeRequest will make a request to the Stripe API with the specific variables given to it
func (rb *Base) MakeRequest(secretKey, path string, params *RequestParameters) ([]byte, error) {
	parsedBaseURL, err := url.Parse(rb.APIBaseURL)
	if err != nil {
		return []byte{}, err
	}

	client := &stripe.Client{
		BaseURL: parsedBaseURL,
		APIKey:  secretKey,
	}

	data, err := rb.buildDataForRequest(params)
	if err != nil {
		return []byte{}, err
	}

	configureReq := func(req *http.Request) {
		rb.setIdempotencyHeader(req, params)
		rb.setStripeAccountHeader(req, params)
		rb.setVersionHeader(req, params)
	}

	resp, err := client.PerformRequest(rb.Method, path, data, configureReq)
	if err != nil {
		return []byte{}, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)

	if !rb.SuppressOutput {
		if err != nil {
			return []byte{}, err
		}

		if rb.showHeaders {
			fmt.Println(rb.formatHeaders(resp))
		}

		result := ansi.ColorizeJSON(string(body), os.Stdout)
		fmt.Println(result)
	}

	return body, nil
}

func (rb *Base) buildDataForRequest(params *RequestParameters) (url.Values, error) {
	data := url.Values{}

	if len(params.data) > 0 || len(params.expand) > 0 {
		for _, datum := range params.data {
			splitDatum := strings.SplitN(datum, "=", 2)

			if len(splitDatum) < 2 {
				return nil, fmt.Errorf("Invalid data argument: %s", datum)
			}

			data.Add(splitDatum[0], splitDatum[1])
		}
		for _, datum := range params.expand {
			data.Add("expand", datum)
		}
	}

	if rb.Method == "GET" {
		if params.limit != "" {
			data.Add("limit", params.limit)
		}
		if params.startingAfter != "" {
			data.Add("starting_after", params.startingAfter)
		}
		if params.endingBefore != "" {
			data.Add("ending_before", params.endingBefore)
		}
	}

	return data, nil
}

func (rb *Base) formatHeaders(response *http.Response) string {
	var allHeaders []string
	for name, headers := range response.Header {
		for _, h := range headers {
			allHeaders = append(allHeaders, fmt.Sprintf("< %v: %v", name, h))
		}
	}
	return strings.Join(allHeaders, "\n") + "\n"
}

func (rb *Base) setIdempotencyHeader(request *http.Request, params *RequestParameters) {
	if params.idempotency != "" {
		request.Header.Set("Idempotency-Key", params.idempotency)
		if rb.Method == "GET" || rb.Method == "DELETE" {
			warning := fmt.Sprintf(
				"Warning: sending an idempotency key with a %s request has no effect and should be avoided, as %s requests are idempotent by definition.",
				rb.Method,
				rb.Method,
			)
			fmt.Println(warning)
		}
	}
}

func (rb *Base) setVersionHeader(request *http.Request, params *RequestParameters) {
	if params.version != "" {
		request.Header.Set("Stripe-Version", params.version)
	}
}

func (rb *Base) setStripeAccountHeader(request *http.Request, params *RequestParameters) {
	if params.stripeAccount != "" {
		request.Header.Set("Stripe-Account", params.stripeAccount)
	}
}

func (rb *Base) confirmCommand() (bool, error) {
	reader := bufio.NewReader(os.Stdin)
	return rb.getUserConfirmation(reader)
}

func (rb *Base) getUserConfirmation(reader *bufio.Reader) (bool, error) {
	if _, needsConfirmation := confirmationCommands[rb.Method]; needsConfirmation && !rb.autoConfirm {
		confirmationPrompt := fmt.Sprintf("Are you sure you want to perform the command: %s?\nEnter 'yes' to confirm: ", rb.Method)
		fmt.Print(confirmationPrompt)

		input, err := reader.ReadString('\n')
		if err != nil {
			return false, err
		}

		return strings.Compare(strings.ToLower(input), "yes\n") == 0, nil
	}

	// Always confirm the command if it does not require explicit user confirmation
	return true, nil
}

func normalizePath(path string) string {
	if strings.HasPrefix(path, "/v1/") {
		return path
	}
	if strings.HasPrefix(path, "v1/") {
		return "/" + path
	}
	if strings.HasPrefix(path, "/") {
		return "/v1" + path
	}
	return "/v1/" + path
}
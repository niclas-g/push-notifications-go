package pushnotifications

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/dgrijalva/jwt-go"
	"github.com/pkg/errors"
)

// The Pusher Push Notifications Server API client
type PushNotifications interface {
	// Publishes notifications to all devices subscribed to at least 1 of the interests given
	// Returns a non-empty `publishId` JSON string if successful; or a non-nil `error` otherwise.
	PublishToInterests(interests []string, request map[string]interface{}) (publishId string, err error)
	// An alias for `PublishToInterests`
	Publish(interests []string, request map[string]interface{}) (publishId string, err error)
	// Publishes notifications to all devices subscribed to at least 1 of the user ids given
	// Returns a non-empty `publishId` JSON string successful, or a non-nil `error` otherwise.
	PublishToUsers(users []string, request map[string]interface{}) (publishId string, err error)
	// Creates a signed JWT for a user id.
	// Returns a signed JWT if successful, or a non-nil `error` otherwise.
	AuthenticateUser(userId string) (string, error)
}

const (
	defaultRequestTimeout     = time.Minute
	defaultBaseEndpointFormat = "https://%s.pushnotifications.pusher.com"
	maxUserIdLength           = 164
	maxNumUserIds             = 1000
)

var (
	interestValidationRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-=@,.;]+$`)
)

type pushNotifications struct {
	InstanceId string
	SecretKey  string

	baseEndpoint string
	httpClient   *http.Client
}

// Creates a New `PushNotifications` instance.
// Returns an non-nil error if `instanceId` or `secretKey` are empty
func New(instanceId string, secretKey string) (PushNotifications, error) {
	if instanceId == "" {
		return nil, errors.New("Instance Id cannot be an empty string")
	}
	if secretKey == "" {
		return nil, errors.New("Secret Key cannot be an empty string")
	}

	return &pushNotifications{
		InstanceId: instanceId,
		SecretKey:  secretKey,

		baseEndpoint: fmt.Sprintf(defaultBaseEndpointFormat, instanceId),
		httpClient: &http.Client{
			Timeout: defaultRequestTimeout,
		},
	}, nil
}

type publishResponse struct {
	PublishId string `json:"publishId"`
}

type publishErrorResponse struct {
	Error       string `json:"error"`
	Description string `json:"description"`
}

func (pn *pushNotifications) AuthenticateUser(userId string) (string, error) {
	if len(userId) == 0 {
		return "", errors.New("User Id cannot be empty")
	}

	if len(userId) > maxUserIdLength {
		return "", errors.Errorf(
			"User Id ('%s') length too long (expected fewer than %d characters, got %d)",
			userId, maxUserIdLength+1, len(userId))
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": userId,
		"exp": time.Now().Add(24 * time.Hour).Unix(),
		"iss": "https://" + pn.InstanceId + ".pushnotifications.pusher.com",
	})

	tokenString, signingErrorErr := token.SignedString([]byte(pn.SecretKey))
	if signingErrorErr != nil {
		return "", errors.Wrap(signingErrorErr, "Failed to sign the JWT token used for User Authentication")
	}

	return tokenString, nil
}

func (pn *pushNotifications) Publish(interests []string, request map[string]interface{}) (string, error) {
	return pn.PublishToInterests(interests, request)
}

func (pn *pushNotifications) PublishToInterests(interests []string, request map[string]interface{}) (string, error) {
	if len(interests) == 0 {
		// this request was not very interesting :/
		return "", errors.New("No interests were supplied")
	}

	if len(interests) > 10 {
		return "",
			errors.Errorf("Too many interests supplied (%d): API only supports up to 10", len(interests))
	}

	for _, interest := range interests {
		if len(interest) == 0 {
			return "", errors.New("An empty interest name is not valid")
		}

		if len(interest) > 164 {
			return "",
				errors.Errorf("Interest length is %d which is over 164 characters", len(interest))
		}

		if !interestValidationRegex.MatchString(interest) {
			return "",
				errors.Errorf(
					"Interest `%s` contains an forbidden character: "+
						"Allowed characters are: ASCII upper/lower-case letters, "+
						"numbers or one of _-=@,.:",
					interest)
		}
	}

	request["interests"] = interests
	bodyRequestBytes, err := json.Marshal(request)
	if err != nil {
		return "", errors.Wrap(err, "Failed to marshal the publish request JSON body")
	}

	url := fmt.Sprintf(pn.baseEndpoint+"/publish_api/v1/instances/%s/publishes", pn.InstanceId)
	return pn.publishToAPI(url, bodyRequestBytes)
}

func (pn *pushNotifications) PublishToUsers(users []string, request map[string]interface{}) (string, error) {
	if len(users) == 0 {
		return "", errors.New("Must supply at least one user id")
	}
	if len(users) > maxNumUserIds {
		return "", errors.New(
			fmt.Sprintf("Too many user ids supplied. API supports up to %d, got %d", maxNumUserIds, len(users)),
		)
	}
	for i, userId := range users {
		if userId == "" {
			return "", errors.New("Empty user ids are not valid")
		}
		if len(userId) > maxUserIdLength {
			return "", errors.New(
				fmt.Sprintf("User Id ('%s') length too long (expected fewer than %d characters, got %d)", userId, maxUserIdLength, len(userId)),
			)
		}
		// test for invalid characters
		if !utf8.ValidString(userId) {
			return "", errors.New(fmt.Sprintf("User Id at index %d is not valid utf8", i))
		}
	}

	request["users"] = users
	bodyRequestBytes, err := json.Marshal(request)
	if err != nil {
		return "", errors.Wrap(err, "Failed to marshal the publish request JSON body")
	}

	url := fmt.Sprintf("%s/publish_api/v1/instances/%s/publishes/users", pn.baseEndpoint, pn.InstanceId)
	return pn.publishToAPI(url, bodyRequestBytes)
}

func (pn *pushNotifications) publishToAPI(url string, bodyRequestBytes []byte) (string, error) {
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyRequestBytes))
	if err != nil {
		return "", errors.Wrap(err, "Failed to prepare the publish request")
	}

	httpReq.Header.Add("Authorization", "Bearer "+pn.SecretKey)
	httpReq.Header.Add("Content-Type", "application/json")
	httpReq.Header.Add("X-Pusher-Library", "pusher-push-notifications-go "+sdkVersion)

	httpResp, err := pn.httpClient.Do(httpReq)
	if err != nil {
		return "", errors.Wrap(err, "Failed to publish notifications due to a network error")
	}

	defer httpResp.Body.Close()
	responseBytes, err := ioutil.ReadAll(httpResp.Body)
	if err != nil {
		return "", errors.Wrap(err, "Failed to read publish notification response due to a network error")
	}

	switch httpResp.StatusCode {
	case http.StatusOK:
		pubResponse := &publishResponse{}
		err = json.Unmarshal(responseBytes, pubResponse)
		if err != nil {
			return "", errors.Wrap(err, "Failed to read publish notification response due to invalid JSON")
		}

		return pubResponse.PublishId, nil
	default:
		pubErrorResponse := &publishErrorResponse{}
		err = json.Unmarshal(responseBytes, pubErrorResponse)
		if err != nil {
			return "", errors.Wrap(err, "Failed to read publish notification response due to invalid JSON")
		}

		errorMessage := fmt.Sprintf("%s: %s", pubErrorResponse.Error, pubErrorResponse.Description)
		return "", errors.Wrap(errors.New(errorMessage), "Failed to publish notification")
	}
}

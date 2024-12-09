package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/sendgrid/sendgrid-go"
	"github.com/sendgrid/sendgrid-go/helpers/mail"
	secretmanagerpb "google.golang.org/genproto/googleapis/cloud/secretmanager/v1"
)

type Config struct {
	EmailAddress string `json:"emailAddress"`
	EmailName    string `json:"emailName"`
	TemplateID   string `json:"templateID"`
	Expired      string `json:"expired"`
	Inactive     string `json:"inactive"`
}

var config Config

func init() {
	zerolog.LevelFieldName = "severity"
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs

	// Load configuration from file
	err := loadConfig("config.json")
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load config file")
	}
}

func loadConfig(filename string) error {
	wd, err := os.Getwd()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to get working directory")
	}

	configPath := filepath.Join(wd, filename)
	file, err := os.Open(configPath)
	if err != nil {
		return fmt.Errorf("failed to open config file: %w", err)
	}
	defer file.Close()

	bytes, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	return json.Unmarshal(bytes, &config)
}

func main() {
	// Initialize Secret variables from Google Secrets Manager
	secretClient, _ := secretmanager.NewClient(context.Background())
	databaseName := mustAccessSecret("mysql-database-name", secretClient)
	databaseUser := mustAccessSecret("mysql-user-auth-user", secretClient)
	dbPassword := mustAccessSecret("mysql-user-auth-pw", secretClient)
	sendGridAPIkey := mustAccessSecret("sendGrid-API", secretClient)

	// Connect on Data Base
	dbClient := dbClient{}
	err := dbClient.connectDB(databaseUser, dbPassword, databaseName)
	if err != nil {
		time.Sleep(time.Second * 25)

		err := dbClient.connectDB(databaseUser, dbPassword, databaseName)
		if err != nil {
			log.Fatal().Err(err).Msgf("Could not connect DB")
		}
	}
	defer dbClient.db.Close()

	// Get Expired Users
	expiredUser, err := dbClient.getExpiredUsers()
	if err != nil {
		log.Fatal().Err(err).Msg("getExpiredUsers")
	}

	if len(expiredUser) > 0 {
		log.Debug().Interface("expiredUsers", expiredUser).Msg("expiredUsers")

		// Deactivate Expired users
		for _, user := range expiredUser {
			log.Debug().Str("Deactivating User:", user.FirstName+" "+user.LastName).Send()
			// Deactivate user
			err := dbClient.setDeactiveUser(user.userID, config.Expired)
			if err != nil {
				log.Error().Err(err).Msg("setDeactiveUser")
				continue
			}

			log.Debug().Str("Deactivated", user.FirstName+" "+user.LastName).Send()
		}
	}

	//Get Job Options
	jobOptions, err := dbClient.getCronJobOptions()
	if err != nil {
		log.Fatal().Err(err).Msg("getToExpireUsers")
	}

	//Check if send notifications is enabled
	if jobOptions.SendNotificationDeactivate {
		log.Debug().Interface("ToExpireUsers", expiredUser).Msg("getToExpireUsers")

		// Get user About to Expire
		groupedUser, err := dbClient.getToExpireUsers()
		if err != nil {
			log.Fatal().Err(err).Msg("getToExpireUsers")
		}

		// Send Notification
		for _, user := range groupedUser {
			if err := sendNotification(user, sendGridAPIkey); err != nil {
				log.Error().Err(err).Msgf("Failed to send notification to %s", user.Email)
			}
		}
	}

	//Check if Auto Inactive is Enabled
	if jobOptions.EnableAutoInactive {
		inactiveTransactionUsers, err := dbClient.getInactiveTransactionUsers()
		if err != nil {
			log.Fatal().Err(err).Msg("getInactiveTransactionUsers")
		}

		log.Debug().Interface("InactiveUsers", inactiveTransactionUsers).Msg("InactiveUsers")

		// Deactivate Inactive users
		for _, user := range inactiveTransactionUsers {
			log.Debug().Str("Deactivating Inactive Transactions Users:", user.FirstName+" "+user.LastName).Send()
			// Deactivate user
			err := dbClient.setDeactiveUser(user.userID, config.Inactive)
			if err != nil {
				log.Error().Err(err).Msg("setDeactiveUser")
				continue
			}

			log.Debug().Str("Deactivated", user.FirstName+" "+user.LastName).Send()
		}
	}
}

func mustAccessSecret(secretName string, client *secretmanager.Client) string {
	// Build the request.
	name := fmt.Sprintf("projects/606995045325/secrets/%s/versions/latest", secretName)
	req := &secretmanagerpb.AccessSecretVersionRequest{
		Name: name,
	}

	// Call the API.
	result, err := client.AccessSecretVersion(context.Background(), req)
	if err != nil {
		log.Fatal().Err(fmt.Errorf("failed to access secret version: %v", err)).Msgf("Failed to get secret %s", secretName)
		panic(err)
	}

	// WARNING: Do not print the secret in a production environment
	return string(result.Payload.Data)
}

func sendNotification(user groupedUser, sendGridAPIkey string) error {
	log.Debug().Str("Send Email to:", user.Email).Send()

	// Create SendGrid object
	m := mail.NewV3Mail()
	e := mail.NewEmail(config.EmailName, config.EmailAddress)
	m.SetFrom(e)

	m.SetTemplateID(config.TemplateID)

	// E-mail set up
	p := mail.NewPersonalization()
	tos := []*mail.Email{
		mail.NewEmail(user.LastName, user.Email),
	}
	p.AddTos(tos...)
	p.SetDynamicTemplateData("accounts", user.Accounts)
	m.AddPersonalizations(p)

	// Create Requisition
	request := sendgrid.GetRequest(sendGridAPIkey, "/v3/mail/send", "https://api.sendgrid.com")
	request.Method = "POST"
	request.Body = mail.GetRequestBody(m)

	// Send E-mail
	_, err := sendgrid.API(request)
	if err != nil {
		log.Error().Err(err).Msg("Failed to send email")
		return err
	}

	log.Debug().Str("Email sent to", user.Email).Send()
	return nil
}

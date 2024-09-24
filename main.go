package main

import (
	"context"
	"fmt"
	"time"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/sendgrid/sendgrid-go"
	"github.com/sendgrid/sendgrid-go/helpers/mail"
	secretmanagerpb "google.golang.org/genproto/googleapis/cloud/secretmanager/v1"
)

func init() {
	zerolog.LevelFieldName = "severity"
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs
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

	log.Debug().Interface("expiredUsers", expiredUser).Msg("expiredUsers")

	// Deactivate Expired users
	for _, user := range expiredUser {
		log.Debug().Str("Deactivating User:", user.FirstName+" "+user.LastName).Send()
		// Deactivate user
		err := dbClient.setDeactiveUser(user.userID)
		if err != nil {
			log.Error().Err(err).Msg("setDeactiveUser")
			continue
		}

		log.Debug().Str("Deactivated", user.FirstName+" "+user.LastName).Send()
	}

	// Get user About to Expire
	groupedUser, err := dbClient.getToExpireUsers()
	if err != nil {
		log.Fatal().Err(err).Msg("getToExpireUsers")
	}

	// Send Notification
	for _, user := range groupedUser {
		log.Debug().Str("Send Email to:", user.Email).Send()

		m := mail.NewV3Mail()
		address := "gemini@cintas.com"
		name := "GEMINI Cintas"
		e := mail.NewEmail(name, address)
		m.SetFrom(e)

		m.SetTemplateID("d-65cd98df82ec49b48e1461e3575bb244")

		p := mail.NewPersonalization()
		tos := []*mail.Email{
			mail.NewEmail(user.LastName, user.Email),
		}
		p.AddTos(tos...)
		p.SetDynamicTemplateData("accounts", user.Accounts)
		m.AddPersonalizations(p)

		request := sendgrid.GetRequest(sendGridAPIkey, "/v3/mail/send", "https://api.sendgrid.com")
		request.Method = "POST"
		var Body = mail.GetRequestBody(m)
		request.Body = Body
		_, err = sendgrid.API(request)
		if err != nil {
			log.Error().Err(err).Send()
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

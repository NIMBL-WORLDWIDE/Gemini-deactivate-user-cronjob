package main

import (
	"context"
	"fmt"
	"time"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
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

	// CONNECT DATABASE
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

	expiredUser, err := dbClient.getUniqueExpiredUsers()
	if err != nil {
		log.Fatal().Err(err).Msg("getUniqueExpiredUsers")
	}

	log.Debug().Interface("expiredUsers", expiredUser).Msg("expiredUsers")

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

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

const (
	SendNotificationDeactivate = "SENDNOTIFICATIONDEACTIVATE"
	EnableAutoInactive         = "ENABLEAUTOINACTIVE"
	EnableTestRun              = "ENABLETESTRUN"
	DaysForUserExpire          = "DAYSFORUSEREXPIRE"
	HcDaysInactive             = "HCDAYSINACTIVE"
	NoHcDaysInactive           = "NOHCDAYSINACTIVE"
	TestRunEmail               = "TESTRUNEMAIL"
)

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

	//Get Job Options
	jobOptions, err := dbClient.getCronJobOptions()
	if err != nil {
		log.Fatal().Err(err).Msg("getToExpireUsers")
	}

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

	//Get Inactive Users Without Transactions
	inactiveTransactionUsers, err := dbClient.getInactiveTransactionUsers()
	if err != nil {
		log.Fatal().Err(err).Msg("getInactiveTransactionUsers")
	}

	//Check if TestRun is Enabled
	if (jobOptions.EnableTestRun) && (len(inactiveTransactionUsers) > 0 || len(expiredUser) > 0) {
		// Combine the two lists into a single slice
		combinedUsers := append(inactiveTransactionUsers, expiredUser...)

		excelBuffer, err := createExcelInMemory(combinedUsers)
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to create Excel file in memory")
		}

		if err := sendNotificationTestRun(jobOptions, excelBuffer, sendGridAPIkey); err != nil {
			log.Error().Err(err).Msgf("Failed to send notification to %s", jobOptions.TestRunEmail)
		}
	}

	// Check if auto-deactivation for inactive users is enabled and if there are users to process
	if jobOptions.EnableAutoInactive && len(inactiveTransactionUsers) > 0 {
		log.Debug().
			Int("userCount", len(inactiveTransactionUsers)).
			Msg("Starting bulk deactivation of inactive transaction users")

		// Perform bulk deactivation
		if err := dbClient.setDeactiveUsersBulk(inactiveTransactionUsers, config.Inactive); err != nil {
			log.Error().
				Err(err).
				Msg("Failed to deactivate inactive transaction users")
		} else {
			log.Debug().
				Int("userCount", len(inactiveTransactionUsers)).
				Msg("Successfully deactivated inactive transaction users")
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

func sendNotificationTestRun(opt *jobOptions, attachment *bytes.Buffer, sendGridAPIkey string) error {
	log.Debug().Str("Send Email to:", opt.TestRunEmail).Send()

	// Create SendGrid object
	m := mail.NewV3Mail()
	e := mail.NewEmail(config.EmailName, config.EmailAddress)
	m.SetFrom(e)

	m.Subject = "Technical Notification" // Define a subject specific for technical users
	content := mail.NewContent("text/plain", "Dear Technical User,\n\nPlease find the attached file for your review. \n\nThe users in the attachment will be deactivated .\n\nBest regards,\nTeam")
	m.AddContent(content)

	// E-mail set up
	p := mail.NewPersonalization()
	emails := strings.Split(opt.TestRunEmail, ";") // Split emails by `;`

	for _, email := range emails {
		email = strings.TrimSpace(email) // Remove blank spaces
		if email != "" {
			p.AddTos(mail.NewEmail(email, email)) // Add each e-mail
		}
	}

	m.AddPersonalizations(p)

	// Add attachment if provided
	if attachment != nil {
		fileAttachment := mail.NewAttachment()
		encodedContent := base64.StdEncoding.EncodeToString(attachment.Bytes())
		fileAttachment.SetContent(encodedContent)
		fileAttachment.SetType("application/vnd.ms-excel")
		fileAttachment.SetFilename("deactive_users.csv")
		fileAttachment.SetDisposition("attachment")
		m.AddAttachment(fileAttachment)
	}

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

	log.Debug().Str("Email sent to", opt.TestRunEmail).Send()
	return nil
}

func createExcelInMemory(users []deactiveUsers) (*bytes.Buffer, error) {
	buffer := &bytes.Buffer{}
	writer := csv.NewWriter(buffer)
	defer writer.Flush()

	// Write header row
	header := []string{"UserID", "FirstName", "LastName", "Reason"}
	if err := writer.Write(header); err != nil {
		return nil, fmt.Errorf("error writing header: %w", err)
	}

	// Write user data
	for _, user := range users {
		row := []string{
			fmt.Sprintf("%d", user.userID),
			user.FirstName,
			user.LastName,
			user.Reason,
		}
		if err := writer.Write(row); err != nil {
			return nil, fmt.Errorf("error writing row: %w", err)
		}
	}

	return buffer, nil
}

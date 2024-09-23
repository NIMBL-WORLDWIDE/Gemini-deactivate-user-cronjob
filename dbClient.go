package main

import (
	"database/sql"
	"fmt"

	_ "github.com/go-sql-driver/mysql"
	"github.com/rs/zerolog/log"
)

type dbClient struct {
	db *sql.DB
}

func (c *dbClient) connectDB(user string, password string, dbName string) error {
	log.Debug().Msg("CONNECTING CLOUD SQL DB...")
	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?parseTime=true",
		user,
		password,
		"127.0.0.1:3306",
		dbName)

	var err error
	c.db, err = sql.Open("mysql", dsn)
	if err != nil {
		return err
	}

	err = c.db.Ping()
	if err != nil {
		return err
	}

	log.Debug().Msg("CONNECTED")
	return nil
}

type expiredUser struct {
	userID    int
	FirstName string
	LastName  string
}

func (c *dbClient) getUniqueExpiredUsers() (users []expiredUser, err error) {
	query := `SELECT userID,FirstName,LastName FROM user
	          WHERE active = 1
			    AND expirationDate IS NOT NULL
				AND expirationDate < CURDATE()
	`
	rows, err := c.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("erros executing query: %w", err)
	}
	defer func() {
		if rows != nil {
			rows.Close()
		}
	}()

	for rows.Next() {
		var user expiredUser
		err = rows.Scan(
			&user.userID,
			&user.FirstName,
			&user.LastName,
		)

		if err != nil {
			return
		}

		users = append(users, user)
	}

	return users, nil
}

func (c *dbClient) setDeactiveUser(userID int) error {
	updQuery := `UPDATE user SET active = 0 
		          WHERE userID = ?
	`
	insQuery := `
		INSERT INTO deactivatedUser (userID, deactivatedDate, deactivatedBy) 
		VALUES (?, CURDATE(), 'DEACTIVATED_CRONJOB')
	`

	// Start Transaction
	tx, err := c.db.Begin()
	if err != nil {
		return fmt.Errorf("error starting transaction: %w", err)
	}

	// Transaction Control
	defer func() {
		if err != nil {
			// If is there any error Rollback
			tx.Rollback()
		} else {
			// Else Commit
			tx.Commit()
		}
	}()

	// Update user with active 0
	if _, err = tx.Exec(updQuery, userID); err != nil {
		return fmt.Errorf("error deactivating user with ID %d: %w", userID, err)
	}

	// Inset user on control table
	if _, err = tx.Exec(insQuery, userID); err != nil {
		return fmt.Errorf("error inserting deactivated user with ID %d: %w", userID, err)
	}

	return nil
}

package main

import (
	"database/sql"
	"fmt"
	"time"

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

func (c *dbClient) getExpiredUsers() (users []expiredUser, err error) {
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

type toExpireUser struct {
	accountDesc    string
	userID         int
	FirstName      string
	LastName       string
	expirationDate *time.Time
	accountNum     int
	userAuthID     int
	email          string
	cardID         int
	active         int
	authName       string
}

type accountInfo struct {
	AccountDesc    string
	FirstName      string
	LastName       string
	ExpirationDate string
	CardID         int
	Active         string
}

type groupedUser struct {
	UserAuthID int
	Email      string
	LastName   string
	Accounts   []accountInfo
}

func (c *dbClient) getToExpireUsers() (groupedUsers []groupedUser, err error) {
	query := `SELECT accDesc.accountDesc, user.userID, user.FirstName, user.LastName, user.expirationDate, user.accountNum,
				     auth.userAuthID, auth.email, user.cardID, user.active,auth.LastName as authName 
			  FROM user
			  INNER JOIN account accDesc      on accDesc.accountNum = user.accountNum    
			  INNER JOIN userAuthAccounts acc on acc.accountNum     = user.accountNum
			  INNER JOIN userAuth auth        on auth.userAuthID    = acc.userAuthID                  
			  WHERE user.expirationDate IS NOT NULL
			  AND user.expirationDate = ( SELECT DATE_ADD(CURDATE(), INTERVAL (SELECT value FROM config WHERE PARAM = 'DAYSFORUSEREXPIRE') DAY) )
			  AND auth.userAccessRoleID IN (5, 6)`

	rows, err := c.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("error executing query: %w", err)
	}
	defer rows.Close()

	// Map to hold grouped users by userAuthID
	userMap := make(map[int]*groupedUser)

	for rows.Next() {
		var user toExpireUser
		err = rows.Scan(
			&user.accountDesc,
			&user.userID,
			&user.FirstName,
			&user.LastName,
			&user.expirationDate,
			&user.accountNum,
			&user.userAuthID,
			&user.email,
			&user.cardID,
			&user.active,
			&user.authName,
		)

		if err != nil {
			return nil, fmt.Errorf("error scanning row: %w", err)
		}

		// Check if the userAuthID already exists in the map
		if _, exists := userMap[user.userAuthID]; !exists {
			// If not, create a new GroupedUser and add to the map
			userMap[user.userAuthID] = &groupedUser{
				UserAuthID: user.userAuthID,
				Email:      user.email,
				LastName:   user.authName,
				Accounts:   []accountInfo{},
			}
		}

		// Format the expiration date as "YYYY-MM-DD" or "N/A" if nil
		var formattedDate string
		if user.expirationDate != nil {
			formattedDate = user.expirationDate.Format("2006-01-02")
		}

		var status string
		switch user.active {
		case 1:
			status = "Active"
		default:
			status = "Inactive"
		}

		// Add account information to the user's account list
		userMap[user.userAuthID].Accounts = append(userMap[user.userAuthID].Accounts, accountInfo{
			AccountDesc:    user.accountDesc,
			FirstName:      user.FirstName,
			LastName:       user.LastName,
			ExpirationDate: formattedDate,
			CardID:         user.cardID,
			Active:         status,
		})
	}

	// Convert map to slice
	for _, groupedUser := range userMap {
		groupedUsers = append(groupedUsers, *groupedUser)
	}

	return groupedUsers, nil
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

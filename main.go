package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	_ "github.com/lib/pq"
)

type InputData struct {
	FirstName      string `json:"firstName"`
	LastName       string `json:"lastName"`
	CompanyWebsite string `json:"companyWebsite"`
}

type OutputData struct {
	Emails []string `json:"emails"`
}

type DomainSearchInput struct {
	Domain string `json:"domain"`
}

var db *sql.DB

func main() {
	var err error
	db, err = sql.Open("postgres", "postgres://emailfind_user:emailfind@localhost/emailfind?sslmode=disable")
	if err != nil {
		log.Fatalf("Error connecting to the database: %v", err)
	}
	defer db.Close()

	err = db.Ping()
	if err != nil {
		log.Fatalf("Error pinging the database: %v", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS email_cache (
			id SERIAL PRIMARY KEY,
			first_name TEXT,
			last_name TEXT,
			company_website TEXT,
			email TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		log.Fatalf("Error creating table: %v", err)
	}

	r := mux.NewRouter()
	r.HandleFunc("/find-email", findEmailHandler).Methods("POST")
	r.HandleFunc("/search-domain", searchDomainHandler).Methods("POST") // New route
	r.PathPrefix("/").Handler(http.FileServer(http.Dir("./static")))

	log.Println("Server is running on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}

func findEmailHandler(w http.ResponseWriter, r *http.Request) {
	var input InputData
	err := json.NewDecoder(r.Body).Decode(&input)
	if err != nil {
		log.Printf("Error decoding input: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("Received input: %+v", input)

	cachedEmails, err := getCachedEmails(input)
	if err == nil && len(cachedEmails) > 0 {
		log.Printf("Cache hit: %v", cachedEmails)
		json.NewEncoder(w).Encode(OutputData{Emails: cachedEmails})
		return
	}

	emails := generateEmailPermutations(input.FirstName, input.LastName, input.CompanyWebsite)
	log.Printf("Generated %d email permutations", len(emails))

	validEmails := verifyEmailsAsync(emails, input.CompanyWebsite)

	if len(validEmails) > 0 {
		log.Printf("Valid emails found: %v", validEmails)
		err := storeEmails(input, validEmails)
		if err != nil {
			log.Printf("Error storing emails in database: %v", err)
		}
		json.NewEncoder(w).Encode(OutputData{Emails: validEmails})
	} else {
		log.Println("No valid emails found")
		http.Error(w, "No valid emails found", http.StatusNotFound)
	}
}

func generateEmailPermutations(firstName, lastName, domain string) []string {
	firstName = strings.ToLower(firstName)
	lastName = strings.ToLower(lastName)
	domain = strings.ToLower(domain)

	permutations := []string{
		fmt.Sprintf("%s@%s", firstName, domain),
		fmt.Sprintf("%s.%s@%s", firstName, lastName, domain),
		fmt.Sprintf("%s%s@%s", firstName, lastName, domain),
		fmt.Sprintf("%s.%s@%s", firstName[:1], lastName, domain),
		fmt.Sprintf("%s%s@%s", firstName[:1], lastName, domain),
		fmt.Sprintf("%s.%s@%s", firstName, lastName[:1], domain),
		fmt.Sprintf("%s%s@%s", firstName, lastName[:1], domain),
		fmt.Sprintf("%s_%s@%s", firstName, lastName, domain),
		fmt.Sprintf("%s-%s@%s", firstName, lastName, domain),
		fmt.Sprintf("%s@%s", lastName, domain),
		fmt.Sprintf("%s.%s@%s", lastName, firstName, domain),
		fmt.Sprintf("%s%s@%s", lastName, firstName, domain),
		fmt.Sprintf("%s.%s@%s", lastName[:1], firstName, domain),
		fmt.Sprintf("%s%s@%s", lastName[:1], firstName, domain),
		fmt.Sprintf("%s_%s@%s", lastName, firstName, domain),
		fmt.Sprintf("%s-%s@%s", lastName, firstName, domain),
		fmt.Sprintf("%s_%s@%s", firstName[:1], lastName, domain),
		fmt.Sprintf("%s-%s@%s", firstName[:1], lastName, domain),
		fmt.Sprintf("%s_%s@%s", lastName[:1], firstName, domain),
		fmt.Sprintf("%s-%s@%s", lastName[:1], firstName, domain),
		fmt.Sprintf("%s%s@%s", lastName, firstName[:1], domain),
		fmt.Sprintf("%s.%s@%s", lastName, firstName[:1], domain),
		fmt.Sprintf("%s_%s@%s", lastName, firstName[:1], domain),
		fmt.Sprintf("%s-%s@%s", lastName, firstName[:1], domain),
		fmt.Sprintf("%s@%s", firstName[:1], domain),
		fmt.Sprintf("%s@%s", lastName[:1], domain),
		fmt.Sprintf("%s%s@%s", firstName[:1], lastName[:1], domain),
		fmt.Sprintf("%s.%s@%s", firstName[:1], lastName[:1], domain),
		fmt.Sprintf("%s_%s@%s", firstName[:1], lastName[:1], domain),
		fmt.Sprintf("%s-%s@%s", firstName[:1], lastName[:1], domain),
		fmt.Sprintf("%s%s@%s", lastName[:1], firstName[:1], domain),
		fmt.Sprintf("%s.%s@%s", lastName[:1], firstName[:1], domain),
		fmt.Sprintf("%s_%s@%s", lastName[:1], firstName[:1], domain),
		fmt.Sprintf("%s-%s@%s", lastName[:1], firstName[:1], domain),
	}

	return permutations
}

func verifyEmailsAsync(emails []string, domain string) []string {
	var wg sync.WaitGroup
	var mu sync.Mutex
	validEmails := []string{}

	for _, email := range emails {
		wg.Add(1)
		go func(e string) {
			defer wg.Done()
			if verifyEmail(e, domain) {
				mu.Lock()
				validEmails = append(validEmails, e)
				mu.Unlock()
			}
		}(email)
	}

	wg.Wait()
	return validEmails
}

func searchDomainHandler(w http.ResponseWriter, r *http.Request) {
	var input DomainSearchInput
	err := json.NewDecoder(r.Body).Decode(&input)
	if err != nil {
		log.Printf("Error decoding input: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("Received domain search input: %+v", input)

	emails, err := getEmailsByDomain(input.Domain)
	if err != nil {
		log.Printf("Error fetching emails for domain %s: %v", input.Domain, err)
		http.Error(w, "Error fetching emails", http.StatusInternalServerError)
		return
	}

	if len(emails) > 0 {
		log.Printf("Found %d emails for domain %s", len(emails), input.Domain)
		json.NewEncoder(w).Encode(OutputData{Emails: emails})
	} else {
		log.Printf("No emails found for domain %s", input.Domain)
		http.Error(w, "No emails found for the given domain", http.StatusNotFound)
	}
}

func getEmailsByDomain(domain string) ([]string, error) {
	rows, err := db.Query("SELECT DISTINCT email FROM email_cache WHERE company_website LIKE $1", "%"+domain+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var emails []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		emails = append(emails, email)
	}

	return emails, nil
}

func verifyEmail(email, domainName string) bool {
	mxRecords, err := net.LookupMX(domainName)
	if err != nil {
		log.Printf("Error performing MX record lookup for %s: %v", domainName, err)
		return false
	}

	if len(mxRecords) == 0 {
		log.Printf("No MX records found for %s", domainName)
		return false
	}

	mailExchange := strings.TrimSuffix(mxRecords[0].Host, ".")
	serverAddress := mailExchange + ":25"

	conn, err := net.DialTimeout("tcp", serverAddress, 10*time.Second)
	if err != nil {
		log.Printf("Error connecting to %s: %v", serverAddress, err)
		return false
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, domainName)
	if err != nil {
		log.Printf("Error creating SMTP client: %v", err)
		return false
	}
	defer client.Close()

	err = client.Hello("example.com")
	if err != nil {
		log.Printf("Error sending HELO: %v", err)
		return false
	}

	err = client.Mail("test@example.com")
	if err != nil {
		log.Printf("Error sending MAIL FROM: %v", err)
		return false
	}

	err = client.Rcpt(email)
	if err != nil {
		errorMessage := err.Error()
		log.Printf("Error sending RCPT TO for %s: %v", email, err)

		// Check for specific error codes or messages that indicate the email might exist
		if strings.Contains(errorMessage, "450 4.2.1") ||
			strings.Contains(errorMessage, "452 4.2.2") ||
			strings.Contains(errorMessage, "451 4.4.1") ||
			strings.Contains(errorMessage, "421 4.7.0") ||
			strings.Contains(errorMessage, "450 4.7.1") {
			log.Printf("Temporary error received for %s. Considering the email as valid.", email)
			return true
		}

		return false
	}

	log.Printf("Email %s is valid", email)
	return true
}

func getCachedEmails(input InputData) ([]string, error) {
	rows, err := db.Query("SELECT email FROM email_cache WHERE first_name = $1 AND last_name = $2 AND company_website = $3", input.FirstName, input.LastName, input.CompanyWebsite)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var emails []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		emails = append(emails, email)
	}

	return emails, nil
}

func storeEmails(input InputData, emails []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	for _, email := range emails {
		_, err := tx.Exec("INSERT INTO email_cache (first_name, last_name, company_website, email) VALUES ($1, $2, $3, $4)", input.FirstName, input.LastName, input.CompanyWebsite, email)
		if err != nil {
			tx.Rollback()
			return err
		}
	}

	return tx.Commit()
}

//docker run --name emailfind-db -e POSTGRES_USER=emailfind_user -e POSTGRES_PASSWORD=emailfind -e POSTGRES_DB=emailfind -p 5432:5432 -d postgres

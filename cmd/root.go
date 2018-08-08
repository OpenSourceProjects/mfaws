package cmd

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sts"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	ini "gopkg.in/ini.v1"
)

// CredentialsShortTerm is used to reflect updated credentials
type CredentialsShortTerm struct {
	AssumedRole        string `ini:"assumed_role"`
	AssumedRoleARN     string `ini:"assumed_role_arn,omitempty"`
	AWSAccessKeyID     string `ini:"aws_access_key_id"`
	AWSSecretAccessKey string `ini:"aws_secret_access_key"`
	AWSSessionToken    string `ini:"aws_session_token"`
	AWSSecurityToken   string `ini:"aws_security_token"`
	Expiration         string `ini:"expiration"`
}

var rootCmd = &cobra.Command{
	Use:   "mfaws",
	Short: "AWS Multi-Factor Authentication manager",
	Long:  `AWS Multi-Factor Authentication manager`,

	Run: func(cmd *cobra.Command, args []string) {

		cfg, err := ini.Load(viper.GetString("credentials-file"))
		if err != nil {
			os.Exit(1)
		}
		profileLongTerm := viper.GetString("profile") + viper.GetString("long-term-suffix")
		profileShortTerm := viper.GetString("profile") + viper.GetString("short-term-suffix")

		if cfg.Section(profileShortTerm).HasKey("expiration") && !viper.GetBool("force") {
			expirationUnparsed := cfg.Section(profileShortTerm).Key("expiration").String()
			expiration, _ := time.Parse("2006-01-02 15:04:05", expirationUnparsed)
			secondsRemaining := expiration.Unix() - time.Now().Unix()
			if secondsRemaining > 0 {
				fmt.Printf("Credentials for profile `%s` still valid for %d seconds\n", profileShortTerm, secondsRemaining)
				return
			}
		}

		if cfg.Section(profileLongTerm).HasKey("aws_mfa_device") {
			viper.SetDefault("device", cfg.Section(profileLongTerm).Key("aws_mfa_device").String())
		}
		if cfg.Section(profileLongTerm).HasKey("assume_role") {
			viper.SetDefault("assume-role", cfg.Section(profileLongTerm).Key("assume_role").String())
		}

		sess := CreateSession(profileLongTerm)
		var credsShortTerm CredentialsShortTerm
		if len(viper.GetString("assume-role")) == 0 {
			viper.SetDefault("duration", 43200)
			DumpConfig()
			credsShortTerm = GetCredsWithoutRole(sess)
		} else {
			viper.SetDefault("duration", 3600)
			DumpConfig()
			credsShortTerm = GetCredsWithRole(sess)
		}

		err = cfg.Section(profileShortTerm).ReflectFrom(&credsShortTerm)
		if err != nil {
			os.Exit(1)
		}

		err = cfg.SaveTo(viper.GetString("credentials-file"))
		if err != nil {
			os.Exit(1)
		}
		fmt.Printf("Success! Credentials for profile `%s` valid for %d seconds\n", profileShortTerm, viper.GetInt("duration"))
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	currentUser, err := user.Current()
	if err != nil {
		os.Exit(1)
	}

	rootCmd.PersistentFlags().StringP("credentials-file", "c", "", "Path to AWS credentials file (default \"~/.aws/credentials\") [AWS_SHARED_CREDENTIALS_FILE]")
	rootCmd.PersistentFlags().StringP("profile", "p", "", "Name of profile to use in AWS credentials file (default \"default\") [AWS_PROFILE]")
	rootCmd.PersistentFlags().String("long-term-suffix", "", "Suffix appended to long-term profiles (default \"-long-term\")")
	rootCmd.PersistentFlags().String("short-term-suffix", "", "Suffix appended to short-term profiles (default \"\")")
	rootCmd.PersistentFlags().StringP("device", "d", "", "ARN of MFA device to use [MFA_DEVICE]")
	rootCmd.PersistentFlags().StringP("assume-role", "a", "", "ARN of IAM role to assume [MFA_ASSUME_ROLE]")
	rootCmd.PersistentFlags().IntP("duration", "l", 0, "Duration in seconds for credentials to remain valid (default assume-role ? 3600 : 43200) [MFA_STS_DURATION]")
	rootCmd.PersistentFlags().StringP("role-session-name", "s", "", "Session name when assuming a role")
	rootCmd.PersistentFlags().BoolP("force", "f", false, "Force credentials to refresh even if not expired")
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "Enable verbose output")
	rootCmd.PersistentFlags().StringP("token", "t", "", "MFA token to use for authentication")

	viper.BindPFlags(rootCmd.PersistentFlags())

	viper.BindEnv("credentials-file", "AWS_SHARED_CREDENTIALS_FILE")
	viper.BindEnv("profile", "AWS_PROFILE")
	viper.BindEnv("device", "MFA_DEVICE")
	viper.BindEnv("assume-role", "MFA_ASSUME_ROLE")
	viper.BindEnv("duration", "MFA_STS_DURATION")

	viper.SetDefault("credentials-file", filepath.Join(currentUser.HomeDir, ".aws", "credentials"))
	viper.SetDefault("profile", "default")
	viper.SetDefault("long-term-suffix", "-long-term")
	viper.SetDefault("short-term-suffix", "")
	viper.SetDefault("role-session-name", currentUser.Username)
}

// CreateSession creates an AWS session from the given profile
func CreateSession(profileLongTerm string) *session.Session {
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Profile: profileLongTerm,
	}))
	return sess
}

// GetMFAToken retrieves MFA token codes from either stdin or the "token" flag
func GetMFAToken() string {
	var mfaToken string
	if viper.GetString("token") == "" || viper.GetString("token") == "-" {
		fmt.Printf("MFA token code: ")
		_, err := fmt.Scanln(&mfaToken)
		if err != nil {
			os.Exit(1)
		}
	} else {
		mfaToken = viper.GetString("token")
	}
	return mfaToken
}

// GetCredsWithoutRole is used to get temporary AWS credentials when NOT assuming a role
func GetCredsWithoutRole(sess *session.Session) CredentialsShortTerm {

	mfaToken := GetMFAToken()

	input := &sts.GetSessionTokenInput{
		DurationSeconds: aws.Int64(viper.GetInt64("duration")),
		SerialNumber:    aws.String(viper.GetString("device")),
		TokenCode:       aws.String(mfaToken),
	}
	svc := sts.New(sess)
	result, err := svc.GetSessionToken(input)
	if err != nil {
		os.Exit(1)
	}
	creds := result.Credentials
	credsShortTerm := CredentialsShortTerm{
		AssumedRole:        "False",
		AssumedRoleARN:     "",
		AWSAccessKeyID:     *creds.AccessKeyId,
		AWSSecretAccessKey: *creds.SecretAccessKey,
		AWSSessionToken:    *creds.SessionToken,
		AWSSecurityToken:   *creds.SessionToken,
		Expiration:         creds.Expiration.Format("2006-01-02 15:04:05"),
	}
	return credsShortTerm
}

// GetCredsWithRole is used to get temporary AWS credentials when assuming a role
func GetCredsWithRole(sess *session.Session) CredentialsShortTerm {

	mfaToken := GetMFAToken()

	creds := stscreds.NewCredentials(sess, viper.GetString("assume-role"), func(p *stscreds.AssumeRoleProvider) {
		p.Duration = time.Duration(viper.GetInt("duration")) * time.Second
		p.SerialNumber = aws.String(viper.GetString("device"))
		p.TokenCode = aws.String(mfaToken)
		p.RoleSessionName = viper.GetString("role-session-name")
	})
	credsRepsonse, err := creds.Get()
	if err != nil {
		os.Exit(1)
	}
	expirationTime := time.Now().UTC().Add(time.Duration(viper.GetInt("duration")) * time.Second)
	credsShortTerm := CredentialsShortTerm{
		AssumedRole:        "True",
		AssumedRoleARN:     viper.GetString("assume-role"),
		AWSAccessKeyID:     credsRepsonse.AccessKeyID,
		AWSSecretAccessKey: credsRepsonse.SecretAccessKey,
		AWSSessionToken:    credsRepsonse.SessionToken,
		AWSSecurityToken:   credsRepsonse.SessionToken,
		Expiration:         expirationTime.Format("2006-01-02 15:04:05"),
	}
	return credsShortTerm
}

// DumpConfig logs the current viper configuration for debugging
func DumpConfig() {
	if viper.GetBool("verbose") {
		fmt.Printf("credentials-file: %s\n", viper.Get("credentials-file"))
		fmt.Printf("profile: %s\n", viper.Get("profile"))
		fmt.Printf("long-term-suffix: %s\n", viper.Get("long-term-suffix"))
		fmt.Printf("short-term-suffix: %s\n", viper.Get("short-term-suffix"))
		fmt.Printf("device: %s\n", viper.Get("device"))
		fmt.Printf("assume-role: %s\n", viper.Get("assume-role"))
		fmt.Printf("duration: %d\n", viper.Get("duration"))
		fmt.Printf("role-session-name: %s\n", viper.Get("role-session-name"))
		fmt.Printf("force: %t\n", viper.Get("force"))
		fmt.Printf("verbose: %s\n", viper.Get("verbose"))
		fmt.Printf("token: %s\n", viper.Get("token"))
	}
}

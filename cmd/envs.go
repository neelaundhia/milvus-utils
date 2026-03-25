package cmd

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/spf13/cobra"
)

// envsCmd represents the serve command.
var envsCmd = &cobra.Command{
	Use:  "envs",
	Long: "List all config envs",
	Run:  doEnvs,
}

func init() { //nolint:gochecknoinits
	envsCmd.Flags().Bool("defaults", false, "Print default values")
	envsCmd.Flags().Bool("values", false, "Print final resolved values after all overlays (config.yaml, secrets.yaml, --config, env vars)")
	rootCmd.AddCommand(envsCmd)
}

func doEnvs(cmd *cobra.Command, args []string) {
	defaults, err := cmd.Flags().GetBool("defaults")
	if err != nil {
		fmt.Println("Error getting defaults flag:", err)
		return
	}

	values, err := cmd.Flags().GetBool("values")
	if err != nil {
		fmt.Println("Error getting values flag:", err)
		return
	}

	var src reflect.Value
	if values {
		cfg, err := loadConfig()
		if err != nil {
			fmt.Println("Error loading config:", err)
			return
		}
		src = reflect.ValueOf(cfg)
	} else {
		src = reflect.ValueOf(&Config{})
	}

	printEnvVarsRecursive(src, "", defaults, values)
}

// printEnvVarsRecursive recursively processes a struct and prints environment variable names
func printEnvVarsRecursive(v reflect.Value, prefix string, printDefaults bool, printValues bool) {
	// Get the actual value if v is a pointer
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}

	// Only process structs
	if v.Kind() != reflect.Struct {
		return
	}

	t := v.Type()

	// Iterate through all fields in the struct
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fieldValue := v.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Get the mapstructure tag
		tag := field.Tag.Get("mapstructure")
		if tag == "" || tag == "-" {
			continue
		}

		// Create environment variable name
		envName := strings.ToUpper(tag)
		if prefix != "" {
			envName = prefix + "_" + envName
		}

		// If the field is a struct, process its fields recursively
		if fieldValue.Kind() == reflect.Struct {
			printEnvVarsRecursive(fieldValue, envName, printDefaults, printValues)
		} else if fieldValue.Kind() == reflect.Ptr && !fieldValue.IsNil() && fieldValue.Elem().Kind() == reflect.Struct {
			printEnvVarsRecursive(fieldValue.Elem(), envName, printDefaults, printValues)
		} else {
			// Print the environment variable name with value if requested
			if printValues {
				fmt.Printf("%s=%v\n", envName, fieldValue.Interface())
			} else if printDefaults {
				defaultValue := field.Tag.Get("default")
				fmt.Printf("%s=%s\n", envName, defaultValue)
			} else {
				fmt.Println(envName)
			}
		}
	}
}

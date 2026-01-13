package command

import (
	"reflect"
	"testing"
)

func TestValidateCommand(t *testing.T) {
	tests := []struct {
		name      string
		cmd       string
		wantError bool
	}{
		{"safe systemctl command", "systemctl reload nginx", false},
		{"safe service command", "service nginx reload", false},
		{"command with semicolon", "systemctl reload nginx; rm -rf /", true},
		{"command with ampersand", "service nginx reload &", true},
		{"command with pipe", "cat /etc/passwd | grep root", true},
		{"command with backticks", "echo `rm -rf /`", true},
		{"command with command substitution", "echo $(rm -rf /)", true},
		{"command with variable substitution", "echo ${HOME}", true},
		{"command with redirect", "echo test > /tmp/file", true},
		{"command with logical and", "echo test && echo test2", true},
		{"command with logical or", "echo test || echo test2", true},
		{"command with sudo", "sudo systemctl reload nginx", true},
		{"empty command", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCommand(tt.cmd)
			if (err != nil) != tt.wantError {
				t.Errorf("validateCommand() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name        string
		cmd         string
		wantCommand string
		wantArgs    []string
		wantError   bool
	}{
		{
			name:        "simple command",
			cmd:         "systemctl reload nginx",
			wantCommand: "systemctl",
			wantArgs:    []string{"reload", "nginx"},
			wantError:   false,
		},
		{
			name:        "command with path",
			cmd:         "/bin/systemctl reload nginx",
			wantCommand: "/bin/systemctl",
			wantArgs:    []string{"reload", "nginx"},
			wantError:   false,
		},
		{
			name:        "single word command",
			cmd:         "nginx",
			wantCommand: "nginx",
			wantArgs:    []string{},
			wantError:   false,
		},
		{
			name:        "empty command",
			cmd:         "",
			wantCommand: "",
			wantArgs:    nil,
			wantError:   true,
		},
		{
			name:        "command with quoted argument",
			cmd:         `nginx -c "/etc/nginx/nginx.conf"`,
			wantCommand: "nginx",
			wantArgs:    []string{"-c", "/etc/nginx/nginx.conf"},
			wantError:   false,
		},
		{
			name:        "command with quoted argument with spaces",
			cmd:         `nginx -c "/etc/nginx/my server.conf"`,
			wantCommand: "nginx",
			wantArgs:    []string{"-c", "/etc/nginx/my server.conf"},
			wantError:   false,
		},
		{
			name:        "safe injection attempt via quoted argument (harmless string)",
			cmd:         `nginx -s "reload_and_date"`,
			wantCommand: "nginx",
			wantArgs:    []string{"-s", "reload_and_date"},
			wantError:   false,
		},
		{
			name:        "command with single quoted argument",
			cmd:         `bash -c 'echo hello'`,
			wantCommand: "bash",
			wantArgs:    []string{"-c", "echo hello"},
			wantError:   false,
		},
		{
			name:        "command with single quoted path with spaces",
			cmd:         `nginx -c '/etc/nginx/my config.conf'`,
			wantCommand: "nginx",
			wantArgs:    []string{"-c", "/etc/nginx/my config.conf"},
			wantError:   false,
		},
		{
			name:        "mixed quotes",
			cmd:         `echo "hello" 'world'`,
			wantCommand: "echo",
			wantArgs:    []string{"hello", "world"},
			wantError:   false,
		},
		{
			name:      "invalid command with semicolon",
			cmd:       "nginx -s reload; date",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command, args, err := Parse(tt.cmd)
			if (err != nil) != tt.wantError {
				t.Errorf("Parse() error = %v, wantError %v", err, tt.wantError)
				return
			}
			if tt.wantError {
				return
			}

			if command != tt.wantCommand {
				t.Errorf("Parse() command = %q, want %q", command, tt.wantCommand)
			}
			if !reflect.DeepEqual(args, tt.wantArgs) {
				t.Errorf("Parse() args = %q, want %q", args, tt.wantArgs)
			}
		})
	}
}

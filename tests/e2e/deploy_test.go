package edgectl_test

import (
    "bytes"
    "encoding/json"
    "io/ioutil"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "regexp"
    "testing"
)

// runCommand runs a CLI command and fails the test if it errors
func runCommand(t *testing.T, args ...string) string {
    t.Helper()
    cmd := exec.Command("../../edgectl", args...)
    var out bytes.Buffer
    var stderr bytes.Buffer
    cmd.Stdout = &out
    cmd.Stderr = &stderr
    err := cmd.Run()
    if err != nil {
        t.Fatalf("Failed to run 'edgectl %s': %v\nstderr: %s", strings.Join(args, " "), err, stderr.String())
    }
    return out.String()
}

// createTempApp creates a temporary directory representing an app version
func createTempApp(t *testing.T, appName, version string) string {
    t.Helper()
    tempDir, err := ioutil.TempDir("", appName+"-"+version)
    if err != nil {
        t.Fatalf("Failed to create temp dir for %s: %v", appName, err)
    }

    // Dummy margo.yaml
    margoContent := []byte("name: " + appName + "\nversion: " + version + "\ncomponents: []\n")
    err = ioutil.WriteFile(filepath.Join(tempDir, "margo.yaml"), margoContent, 0644)
    if err != nil {
        t.Fatalf("Failed to write margo.yaml: %v", err)
    }

    return tempDir
}

// DeploymentStatus represents a simple structure returned by `edgectl status`
// Adapt this to your actual CO/LO/EN status JSON structure
type DeploymentStatus struct {
    App     string `json:"app"`
    Version string `json:"version"`
    Site    string `json:"site"`
    Status  string `json:"status"` // e.g., "deployed"
}

// checkDeployment validates that the app version is deployed to all sites
func checkDeployment(t *testing.T, app, version string, sites []string) {
    t.Helper()
    out := runCommand(t, "co", "deployments", "status", "--depid", app)
    
    var statuses []DeploymentStatus
    if err := json.Unmarshal([]byte(out), &statuses); err != nil {
        t.Fatalf("Failed to parse JSON status for app %s: %v\nOutput: %s", app, err, out)
    }

    siteMap := make(map[string]DeploymentStatus)
    for _, s := range statuses {
        siteMap[s.Site] = s
    }

    for _, site := range sites {
        st, ok := siteMap[site]
        if !ok {
            t.Errorf("No deployment found for app %s on site %s", app, site)
        } else if st.Version != version || st.Status != "deployed" {
            t.Errorf("Deployment mismatch for app %s on site %s: got version=%s, status=%s; want version=%s, status=deployed",
                app, site, st.Version, st.Status, version)
        }
    }
}

func TestDeployMultipleAppsWithValidation(t *testing.T) {
    apps := map[string][]string{
        "retail/pos": {"1.1.0"},
        "retail/inventory": {"2.0.0"},
        //retail/edge-checkout/0.9.0
        //"retail/app2": {"v1.0.0"},
    }
    sites := []string{"a7f3174c-2aa0-4fb7-8e62-984c5703284d", "siteB"}
    var tempDirs []string

    for app, versions := range apps {
        for _, version := range versions {
            //tempDir := createTempApp(t, app, version)
            //tempDirs = append(tempDirs, tempDir)

            // t.Logf("Adding app %s version %s from %s", app, version, tempDir)
            // runCommand(t, "co add app", "--path", tempDir)

            t.Logf("Deploying app %s version %s to sites %v", app, version, sites)
            s :=  fmt.Sprintf("%s/%s", app, version)
            
            runCommand(t, "co", "deploy", "--app", s, "--site", sites[0], "--deploytype", "helm.v3")
            t.Logf("finished runcommand")
            //deploymentID := extractDeploymentID(t, out)

            //t.Logf("Parsed DeploymentID = %s", deploymentID)

            // Validate deployed state
            checkDeployment(t, app, version, sites)

            // Optional update
            newVersion := version + "-update"
            tempUpdateDir := createTempApp(t, app, newVersion)
            tempDirs = append(tempDirs, tempUpdateDir)

            t.Logf("Updating app %s from %s to %s", app, version, newVersion)
            runCommand(t, "update-app", "--app", app, "--version", newVersion, "--sites", strings.Join(sites, ","))

            // Validate updated deployment
            checkDeployment(t, app, newVersion, sites)
        }
    }

    // Cleanup temp dirs
    for _, dir := range tempDirs {
        os.RemoveAll(dir)
    }
}

func extractDeploymentID(t *testing.T, out string) string {
    t.Helper()

    // 1. Try JSON pattern
    jsonRe := regexp.MustCompile(`"deploymentId"\s*:\s*"([a-f0-9\-]+)"`)
    if m := jsonRe.FindStringSubmatch(out); len(m) == 2 {
        return m[1]
    }

    // 2. Try CLI text output
    textRe := regexp.MustCompile(`(?i)deployment[-_ ]?id[: ]+([a-f0-9\-]+)`)
    if m := textRe.FindStringSubmatch(out); len(m) == 2 {
        return m[1]
    }

    t.Fatalf("Could not extract deployment ID from output:\n%s", out)
    return ""
}

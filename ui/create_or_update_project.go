package main

import (
	"errors"
	"fmt"
	"strings"

	functions "hollishake/godeploy-function"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

func NewCreateOrUpdateProjectDialog(
	window fyne.Window,
	state *functions.State,
	existing *functions.Project,
	onSaved func(functions.Project),
) *dialog.ConfirmDialog {
	name := widget.NewEntry()
	personalAccessToken := widget.NewPasswordEntry()
	repositoryURL := widget.NewEntry()
	branch := widget.NewEntry()
	gitUsername := widget.NewEntry()
	gitEmail := widget.NewEntry()
	buildCommand := widget.NewEntry()
	buildCommand.SetPlaceHolder("Leave blank to use the package.json build script")
	var environmentVariables map[string]string
	title := "Create project"
	confirmLabel := "Create"
	if existing != nil {
		title = "Update project"
		confirmLabel = "Save"
		name.SetText(existing.Name)
		personalAccessToken.SetText(existing.PersonalAccessToken)
		repositoryURL.SetText(existing.RepositoryURL)
		branch.SetText(existing.Branch)
		gitUsername.SetText(existing.GitUsername)
		gitEmail.SetText(existing.GitEmail)
		buildCommand.SetText(existing.BuildCommand)
		if existing.EnvironmentVariables != nil {
			environmentVariables = make(map[string]string, len(existing.EnvironmentVariables))
			for key, value := range existing.EnvironmentVariables {
				environmentVariables[key] = value
			}
		}
	}
	environmentStatus := widget.NewLabel(environmentVariablesStatus(environmentVariables))
	clearEnvironmentVariables := widget.NewButton("Clear", func() {
		environmentVariables = make(map[string]string)
		environmentStatus.SetText(environmentVariablesStatus(environmentVariables))
	})
	chooseEnvironmentFile := widget.NewButton("Choose .env file", func() {
		fileDialog := dialog.NewFileOpen(func(file fyne.URIReadCloser, err error) {
			if err != nil {
				dialog.ShowError(err, window)
				return
			}
			if file == nil {
				return
			}

			variables, readErr := functions.ParseEnvironmentFile(file)
			closeErr := file.Close()
			if readErr != nil {
				dialog.ShowError(readErr, window)
				return
			}
			if closeErr != nil {
				dialog.ShowError(fmt.Errorf("close environment file: %w", closeErr), window)
				return
			}
			environmentVariables = variables
			environmentStatus.SetText(environmentVariablesStatus(environmentVariables))
		}, window)
		fileDialog.Show()
	})
	environmentFileInput := container.NewVBox(
		container.NewHBox(chooseEnvironmentFile, clearEnvironmentVariables),
		environmentStatus,
	)
	form := widget.NewForm(
		widget.NewFormItem("Name", name),
		widget.NewFormItem("Access token", personalAccessToken),
		widget.NewFormItem("Repository URL", repositoryURL),
		widget.NewFormItem("Branch", branch),
		widget.NewFormItem("Git username", gitUsername),
		widget.NewFormItem("Git email", gitEmail),
		widget.NewFormItem("Build command", buildCommand),
		widget.NewFormItem("Environment variables", environmentFileInput),
	)
	subtitle := widget.NewLabel("Add a repository to your GoDeploy workspace.")
	subtitle.TextStyle = fyne.TextStyle{Italic: true}
	modalContent := container.NewPadded(container.NewVBox(
		subtitle,
		form,
	))

	modal := dialog.NewCustomConfirm(title, confirmLabel, "Cancel", modalContent, func(confirmed bool) {
		if !confirmed {
			return
		}
		project := functions.Project{
			Name:                 strings.TrimSpace(name.Text),
			PersonalAccessToken:  strings.TrimSpace(personalAccessToken.Text),
			RepositoryURL:        strings.TrimSpace(repositoryURL.Text),
			Branch:               strings.TrimSpace(branch.Text),
			GitUsername:          strings.TrimSpace(gitUsername.Text),
			GitEmail:             strings.TrimSpace(gitEmail.Text),
			BuildCommand:         strings.TrimSpace(buildCommand.Text),
			EnvironmentVariables: environmentVariables,
		}
		if project.Name == "" || project.RepositoryURL == "" || project.Branch == "" {
			dialog.ShowError(errors.New("name, repository URL, and branch are required"), window)
			return
		}
		var err error
		if existing == nil {
			err = functions.CreateNewProject(state, project)
		} else {
			err = functions.UpdateProject(state, existing.Name, project)
		}
		if err != nil {
			dialog.ShowError(err, window)
			return
		}
		if err := state.Reload(); err != nil {
			dialog.ShowError(err, window)
			return
		}
		onSaved(project)
	}, window)
	modal.Resize(fyne.NewSize(640, 640))
	return modal
}

func environmentVariablesStatus(variables map[string]string) string {
	if len(variables) == 0 {
		return "No environment variables loaded"
	}
	return fmt.Sprintf("%d environment variables loaded", len(variables))
}

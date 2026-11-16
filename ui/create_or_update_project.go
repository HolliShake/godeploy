package main

import (
	"errors"
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
	}
	form := widget.NewForm(
		widget.NewFormItem("Name", name),
		widget.NewFormItem("Access token", personalAccessToken),
		widget.NewFormItem("Repository URL", repositoryURL),
		widget.NewFormItem("Branch", branch),
		widget.NewFormItem("Git username", gitUsername),
		widget.NewFormItem("Git email", gitEmail),
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
			Name:                strings.TrimSpace(name.Text),
			PersonalAccessToken: strings.TrimSpace(personalAccessToken.Text),
			RepositoryURL:       strings.TrimSpace(repositoryURL.Text),
			Branch:              strings.TrimSpace(branch.Text),
			GitUsername:         strings.TrimSpace(gitUsername.Text),
			GitEmail:            strings.TrimSpace(gitEmail.Text),
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
	modal.Resize(fyne.NewSize(640, 520))
	return modal
}

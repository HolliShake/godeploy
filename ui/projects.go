package main

import (
	"fmt"
	"strings"

	functions "hollishake/godeploy-function"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func NewProjectsView(window fyne.Window, state *functions.State, onSelected func(functions.Project)) fyne.CanvasObject {
	projects := append([]functions.Project(nil), state.Config.Projects...)
	filteredProjects := append([]functions.Project(nil), projects...)
	searchQuery := ""
	refreshProjects := func() {}
	projectList := widget.NewList(
		func() int {
			return len(filteredProjects)
		},
		func() fyne.CanvasObject {
			name := widget.NewLabel("")
			name.TextStyle = fyne.TextStyle{Bold: true}
			actions := widget.NewButtonWithIcon("", theme.MoreVerticalIcon(), nil)
			return container.NewHBox(
				name,
				layout.NewSpacer(),
				container.NewCenter(
					container.NewHBox(
						container.NewGridWrap(fyne.NewSize(28, 28), actions),
						container.NewGridWrap(fyne.NewSize(theme.Padding(), 28), widget.NewLabel("")),
					),
				),
			)
		},
		func(id widget.ListItemID, object fyne.CanvasObject) {
			project := filteredProjects[id]
			row := object.(*fyne.Container)
			row.Objects[0].(*widget.Label).SetText(project.Name)
			actionContainer := row.Objects[2].(*fyne.Container)
			actionRow := actionContainer.Objects[0].(*fyne.Container)
			actionGrid := actionRow.Objects[0].(*fyne.Container)
			actions := actionGrid.Objects[0].(*widget.Button)
			actions.OnTapped = func() {
				canvas := fyne.CurrentApp().Driver().CanvasForObject(actions)
				updateItem := fyne.NewMenuItem("Update", func() {
					original := project
					NewCreateOrUpdateProjectDialog(window, state, &original, func(updated functions.Project) {
						projects = append([]functions.Project(nil), state.Config.Projects...)
						refreshProjects()
						onSelected(updated)
					}).Show()
				})
				deleteItem := fyne.NewMenuItem("Delete", func() {
					dialog.ShowConfirm(
						"Delete project",
						fmt.Sprintf("Delete %q? This cannot be undone.", project.Name),
						func(confirmed bool) {
							if !confirmed {
								return
							}
							if err := functions.DeleteProject(state, project.Name); err != nil {
								dialog.ShowError(err, window)
								return
							}
							if err := state.Reload(); err != nil {
								dialog.ShowError(err, window)
								return
							}
							projects = append([]functions.Project(nil), state.Config.Projects...)
							refreshProjects()
						},
						window,
					)
				})
				widget.ShowPopUpMenuAtRelativePosition(
					fyne.NewMenu("Project actions", updateItem, deleteItem),
					canvas,
					fyne.NewPos(0, actions.Size().Height),
					actions,
				)
			}
		},
	)
	projectList.OnSelected = func(id widget.ListItemID) {
		onSelected(filteredProjects[id])
	}

	search := widget.NewEntry()
	search.SetPlaceHolder("Search projects...")
	search.Icon = theme.SearchIcon()
	search.OnChanged = func(query string) {
		searchQuery = strings.ToLower(strings.TrimSpace(query))
		refreshProjects()
	}
	refreshProjects = func() {
		filteredProjects = filteredProjects[:0]
		for _, project := range projects {
			if searchQuery == "" ||
				strings.Contains(strings.ToLower(project.Name), searchQuery) ||
				strings.Contains(strings.ToLower(project.Branch), searchQuery) {
				filteredProjects = append(filteredProjects, project)
			}
		}
		projectList.UnselectAll()
		projectList.Refresh()
	}

	addProject := widget.NewButtonWithIcon("", theme.ContentAddIcon(), func() {
		modal := NewCreateOrUpdateProjectDialog(window, state, nil, func(project functions.Project) {
			projects = append([]functions.Project(nil), state.Config.Projects...)
			refreshProjects()
			onSelected(project)
		})
		modal.Show()
	})
	addProjectButton := container.NewGridWrap(fyne.NewSize(40, 40), addProject)
	searchBar := container.NewBorder(nil, nil, nil, addProjectButton, search)
	sidebarHeader := container.NewVBox(
		container.NewHBox(
			widget.NewIcon(theme.FolderOpenIcon()),
			widget.NewLabel("Projects"),
		),
		searchBar,
	)
	return container.NewBorder(
		container.NewPadded(sidebarHeader),
		nil,
		nil,
		nil,
		container.NewPadded(projectList),
	)
}

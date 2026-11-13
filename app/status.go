package app

import (
	"time"

	domain "github.com/slidebolt/sb-domain"
)

func (a *App) healthyDevice(device domain.Device) domain.Device {
	device.Status = &domain.ResourceStatus{Health: "healthy", ObservedAt: time.Now().UTC(), StaleAfterSeconds: int((2 * a.config.Refresh).Seconds())}
	return device
}

func (a *App) healthyEntity(entity domain.Entity) domain.Entity {
	entity.Status = &domain.ResourceStatus{Health: "healthy", ObservedAt: time.Now().UTC(), StaleAfterSeconds: int((2 * a.config.Refresh).Seconds())}
	return entity
}

func connectionHealth(state domain.MediaProviderConnection) *domain.ResourceStatus {
	health := "unavailable"
	if state.Status == "connected" {
		health = "healthy"
	}
	if state.Status == "error" {
		health = "error"
	}
	return &domain.ResourceStatus{Health: health, ObservedAt: time.Now().UTC(), Message: state.LastError}
}

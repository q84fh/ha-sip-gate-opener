"""Sensor platform for SIP Gate Opener integration."""
from __future__ import annotations

import logging
from typing import Any

from homeassistant.components.sensor import SensorEntity
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant, callback
from homeassistant.helpers.entity_platform import AddEntitiesCallback

from .const import (
    DOMAIN,
    STATE_IDLE,
    STATE_CONNECTING,
    STATE_CALLING,
    STATE_RINGING,
    STATE_ANSWERED,
    STATE_BUSY,
    STATE_COMPLETED,
    STATE_FAILED,
)
from .coordinator import SipGateOpenerCoordinator

_LOGGER = logging.getLogger(__name__)

# Status icons mapping
STATUS_ICONS = {
    STATE_IDLE: "mdi:gate",
    STATE_CONNECTING: "mdi:phone-dial",
    STATE_CALLING: "mdi:phone-outgoing",
    STATE_RINGING: "mdi:phone-ring",
    STATE_ANSWERED: "mdi:phone-in-talk",
    STATE_BUSY: "mdi:phone-busy",
    STATE_COMPLETED: "mdi:check-circle",
    STATE_FAILED: "mdi:alert-circle",
}

# Friendly status names
STATUS_NAMES = {
    STATE_IDLE: "Idle",
    STATE_CONNECTING: "Connecting",
    STATE_CALLING: "Calling",
    STATE_RINGING: "Ringing",
    STATE_ANSWERED: "Answered",
    STATE_BUSY: "Busy",
    STATE_COMPLETED: "Completed",
    STATE_FAILED: "Failed",
}


async def async_setup_entry(
    hass: HomeAssistant,
    config_entry: ConfigEntry,
    async_add_entities: AddEntitiesCallback,
) -> None:
    """Set up the SIP Gate Opener sensor."""
    coordinator: SipGateOpenerCoordinator = hass.data[DOMAIN][config_entry.entry_id]
    
    async_add_entities([SipGateOpenerStatusSensor(coordinator, config_entry)], True)


class SipGateOpenerStatusSensor(SensorEntity):
    """Representation of a SIP Gate Opener status sensor."""

    def __init__(self, coordinator: SipGateOpenerCoordinator, config_entry: ConfigEntry) -> None:
        """Initialize the sensor."""
        self._coordinator = coordinator
        self._config_entry = config_entry
        self._attr_name = "Gate Call Status"
        self._attr_unique_id = f"{config_entry.entry_id}_call_status"
        self._attr_icon = STATUS_ICONS.get(coordinator.call_status, "mdi:gate")
        self._attr_native_value = STATUS_NAMES.get(coordinator.call_status, coordinator.call_status)

        # Register for status updates
        self._coordinator.add_status_callback(self._status_updated)

    @property
    def device_info(self) -> dict[str, Any]:
        """Return device information."""
        return {
            "identifiers": {(DOMAIN, self._config_entry.entry_id)},
            "name": "SIP Gate Opener",
            "manufacturer": "SIP Gate Opener",
            "model": "Gate Controller",
            "sw_version": "1.0.0",
        }

    @property
    def extra_state_attributes(self) -> dict[str, Any]:
        """Return additional state attributes."""
        return {
            "gate_number": self._coordinator.gate_number,
            "sip_server": self._coordinator.sip_server,
            "username": self._coordinator.username,
            "raw_status": self._coordinator.call_status,
        }

    @callback
    def _status_updated(self, status: str) -> None:
        """Handle status updates from coordinator."""
        self._attr_icon = STATUS_ICONS.get(status, "mdi:gate")
        self._attr_native_value = STATUS_NAMES.get(status, status)
        self.async_write_ha_state()

    async def async_will_remove_from_hass(self) -> None:
        """Clean up when entity is removed."""
        self._coordinator.remove_status_callback(self._status_updated)
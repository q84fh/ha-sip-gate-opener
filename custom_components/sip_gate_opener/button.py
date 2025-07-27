"""Button platform for SIP Gate Opener integration."""
from __future__ import annotations

import logging
from typing import Any

from homeassistant.components.button import ButtonEntity
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant
from homeassistant.helpers.entity_platform import AddEntitiesCallback

from .const import DOMAIN
from .coordinator import SipGateOpenerCoordinator

_LOGGER = logging.getLogger(__name__)


async def async_setup_entry(
    hass: HomeAssistant,
    config_entry: ConfigEntry,
    async_add_entities: AddEntitiesCallback,
) -> None:
    """Set up the SIP Gate Opener button."""
    coordinator: SipGateOpenerCoordinator = hass.data[DOMAIN][config_entry.entry_id]
    
    async_add_entities([SipGateOpenerButton(coordinator, config_entry)], True)


class SipGateOpenerButton(ButtonEntity):
    """Representation of a SIP Gate Opener button."""

    def __init__(self, coordinator: SipGateOpenerCoordinator, config_entry: ConfigEntry) -> None:
        """Initialize the button."""
        self._coordinator = coordinator
        self._config_entry = config_entry
        self._attr_name = "Open Gate"
        self._attr_unique_id = f"{config_entry.entry_id}_open_gate"
        self._attr_icon = "mdi:gate"

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

    async def async_press(self) -> None:
        """Handle the button press."""
        _LOGGER.info("Gate opener button pressed")
        await self._coordinator.async_open_gate()
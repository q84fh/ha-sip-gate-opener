import logging
import asyncio
from homeassistant.core import HomeAssistant
from homeassistant.config_entries import ConfigEntry
from homeassistant.const import Platform

from .coordinator import SipGateOpenerCoordinator

_LOGGER = logging.getLogger(__name__)

DOMAIN = "sip_gate_opener"
PLATFORMS = [Platform.BUTTON, Platform.SENSOR]

async def async_setup_entry(hass: HomeAssistant, entry: ConfigEntry) -> bool:
    """Set up SIP Gate Opener from a config entry."""
    hass.data.setdefault(DOMAIN, {})
    
    # Create the coordinator instance
    coordinator = SipGateOpenerCoordinator(hass, entry)
    hass.data[DOMAIN][entry.entry_id] = coordinator
    
    await hass.config_entries.async_forward_entry_setups(entry, PLATFORMS)
    return True

async def async_unload_entry(hass: HomeAssistant, entry: ConfigEntry) -> bool:
    """Unload a config entry."""
    unload_ok = await hass.config_entries.async_unload_platforms(entry, PLATFORMS)
    if unload_ok:
        hass.data[DOMAIN].pop(entry.entry_id)
    return unload_ok
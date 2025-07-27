"""Coordinator for SIP Gate Opener integration."""
from __future__ import annotations

import asyncio
import logging
from typing import Any, Callable

from pyVoIP import VoIP
from pyVoIP.VoIP import CallState, InvalidStateError

from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant
from homeassistant.exceptions import HomeAssistantError

from .const import (
    CONF_CALLER_ID,
    CONF_GATE_NUMBER,
    CONF_PASSWORD,
    CONF_SIP_PORT,
    CONF_SIP_SERVER,
    CONF_USERNAME,
    DEFAULT_RING_DURATION,
    STATE_IDLE,
    STATE_CONNECTING,
    STATE_CALLING,
    STATE_RINGING,
    STATE_ANSWERED,
    STATE_BUSY,
    STATE_COMPLETED,
    STATE_FAILED,
)

_LOGGER = logging.getLogger(__name__)


class SipGateOpenerCoordinator:
    """Coordinator for SIP Gate Opener."""

    def __init__(self, hass: HomeAssistant, entry: ConfigEntry) -> None:
        """Initialize the coordinator."""
        self.hass = hass
        self.entry = entry
        self._sip_client: VoIP | None = None
        self._is_calling = False
        self._call_status = STATE_IDLE
        self._status_callbacks: list[Callable[[str], None]] = []

    @property
    def sip_server(self) -> str:
        """Return the SIP server."""
        return self.entry.data[CONF_SIP_SERVER]

    @property
    def sip_port(self) -> int:
        """Return the SIP port."""
        return self.entry.data[CONF_SIP_PORT]

    @property
    def username(self) -> str:
        """Return the username."""
        return self.entry.data[CONF_USERNAME]

    @property
    def password(self) -> str:
        """Return the password."""
        return self.entry.data[CONF_PASSWORD]

    @property
    def gate_number(self) -> str:
        """Return the gate number."""
        return self.entry.data[CONF_GATE_NUMBER]

    @property
    def caller_id(self) -> str | None:
        """Return the caller ID."""
        return self.entry.data.get(CONF_CALLER_ID)

    @property
    def call_status(self) -> str:
        """Return the current call status."""
        return self._call_status

    def add_status_callback(self, callback: Callable[[str], None]) -> None:
        """Add a callback for status updates."""
        self._status_callbacks.append(callback)

    def remove_status_callback(self, callback: Callable[[str], None]) -> None:
        """Remove a status callback."""
        if callback in self._status_callbacks:
            self._status_callbacks.remove(callback)

    def _update_status(self, status: str) -> None:
        """Update the call status and notify callbacks."""
        if self._call_status != status:
            self._call_status = status
            _LOGGER.debug("Call status updated to: %s", status)
            
            # Notify all callbacks
            for callback in self._status_callbacks:
                try:
                    callback(status)
                except Exception as err:
                    _LOGGER.error("Error in status callback: %s", err)

    async def async_open_gate(self) -> None:
        """Open the gate by making a SIP call."""
        if self._is_calling:
            _LOGGER.warning("Already making a call, ignoring new request")
            return

        self._is_calling = True
        try:
            await self._make_sip_call()
        finally:
            self._is_calling = False

    async def _make_sip_call(self) -> None:
        """Make the SIP call to open the gate."""
        _LOGGER.info("Starting SIP call to open gate: %s", self.gate_number)
        
        try:
            self._update_status(STATE_CONNECTING)
            # Run the blocking SIP operations in an executor
            await self.hass.async_add_executor_job(self._blocking_sip_call)
            self._update_status(STATE_COMPLETED)
            _LOGGER.info("Gate opening call completed successfully")
            # Set back to idle after a short delay
            await asyncio.sleep(2)
            self._update_status(STATE_IDLE)
        except Exception as err:
            self._update_status(STATE_FAILED)
            _LOGGER.error("Failed to open gate via SIP call: %s", err)
            # Set back to idle after a short delay
            await asyncio.sleep(3)
            self._update_status(STATE_IDLE)
            raise HomeAssistantError(f"Failed to open gate: {err}") from err

    def _blocking_sip_call(self) -> None:
        """Make the blocking SIP call."""
        # Initialize SIP client
        try:
            # Configure for NAT traversal
            sip_client = VoIP(
                server=self.sip_server,
                port=self.sip_port,
                username=self.username,
                password=self.password,
                calleeID=self.caller_id or self.username,
                # NAT configuration
                myIP=None,  # Auto-detect
                hostname=self.sip_server,
            )
            
            _LOGGER.debug("SIP client initialized, starting call to %s", self.gate_number)
            self._update_status(STATE_CALLING)
            
            # Make the call
            call = sip_client.call(self.gate_number)
            
            # Wait for the call to be established or get busy signal
            # We'll wait a bit to ensure the call is processed
            import time
            start_time = time.time()
            max_wait_time = 10  # Maximum 10 seconds
            
            while time.time() - start_time < max_wait_time:
                try:
                    state = call.state
                    _LOGGER.debug("Call state: %s", state)
                    
                    if state == CallState.ANSWERED:
                        _LOGGER.info("Call answered, waiting for ring duration")
                        self._update_status(STATE_ANSWERED)
                        time.sleep(DEFAULT_RING_DURATION)
                        break
                    elif state == CallState.BUSY:
                        _LOGGER.info("Gate number is busy (expected behavior)")
                        self._update_status(STATE_BUSY)
                        break
                    elif state == CallState.ENDED:
                        _LOGGER.info("Call ended")
                        break
                    elif state in [CallState.RINGING, CallState.TRYING]:
                        if self._call_status != STATE_RINGING:
                            _LOGGER.debug("Call is ringing/trying...")
                            self._update_status(STATE_RINGING)
                        time.sleep(0.5)  # Wait a bit before checking again
                    else:
                        time.sleep(0.1)  # Small delay for other states
                        
                except InvalidStateError:
                    _LOGGER.debug("Call state changed, continuing...")
                    time.sleep(0.1)
                except Exception as e:
                    _LOGGER.error("Error checking call state: %s", e)
                    break
            
            # Hang up the call
            try:
                call.hangup()
                _LOGGER.debug("Call hung up")
            except Exception as e:
                _LOGGER.debug("Error hanging up call (may already be ended): %s", e)
            
            # Stop the SIP client
            try:
                sip_client.stop()
                _LOGGER.debug("SIP client stopped")
            except Exception as e:
                _LOGGER.debug("Error stopping SIP client: %s", e)
                
        except Exception as err:
            _LOGGER.error("SIP call failed: %s", err)
            raise
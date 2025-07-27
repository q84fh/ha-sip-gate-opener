"""Coordinator for SIP Gate Opener integration."""
from __future__ import annotations

import asyncio
import logging
import time
from typing import Any, Callable

from pyVoIP.VoIP import VoIPPhone, CallState, InvalidStateError

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
        self._sip_phone: VoIPPhone | None = None
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
        # Initialize SIP phone
        call_completed = False
        
        def call_callback(call):
            """Callback for handling incoming calls (not used for outgoing)."""
            # This is for incoming calls, we're making outgoing calls
            pass
        
        try:
            _LOGGER.debug("Creating SIP phone instance")
            # Create VoIPPhone instance with correct parameters
            sip_phone = VoIPPhone(
                self.sip_server,
                self.sip_port,
                self.username,
                self.password,
                callCallback=call_callback
            )
            
            _LOGGER.debug("Starting SIP phone")
            sip_phone.start()
            
            # Wait a moment for the phone to initialize
            time.sleep(1)
            
            _LOGGER.debug("SIP phone started, making call to %s", self.gate_number)
            self._update_status(STATE_CALLING)
            
            # Make the call
            call = sip_phone.call(self.gate_number)
            
            # Wait for the call to be established or get busy signal
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
                        call_completed = True
                        break
                    elif state == CallState.BUSY:
                        _LOGGER.info("Gate number is busy (expected behavior)")
                        self._update_status(STATE_BUSY)
                        call_completed = True
                        break
                    elif state == CallState.ENDED:
                        _LOGGER.info("Call ended")
                        call_completed = True
                        break
                    elif state in [CallState.RINGING, CallState.TRYING]:
                        if self._call_status not in [STATE_RINGING, STATE_CALLING]:
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
            
            # If we didn't get a definitive result, assume it worked
            if not call_completed:
                _LOGGER.info("Call timeout reached, assuming gate was triggered")
                call_completed = True
            
            # Hang up the call
            try:
                call.hangup()
                _LOGGER.debug("Call hung up")
            except Exception as e:
                _LOGGER.debug("Error hanging up call (may already be ended): %s", e)
            
            # Stop the SIP phone
            try:
                sip_phone.stop()
                _LOGGER.debug("SIP phone stopped")
            except Exception as e:
                _LOGGER.debug("Error stopping SIP phone: %s", e)
                
        except Exception as err:
            _LOGGER.error("SIP call failed: %s", err)
            # Try to clean up if phone was created
            if 'sip_phone' in locals():
                try:
                    sip_phone.stop()
                except:
                    pass
            raise
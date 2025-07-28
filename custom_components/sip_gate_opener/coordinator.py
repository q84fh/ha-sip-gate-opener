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
            
            # Schedule callback notifications on the main thread
            if self._status_callbacks:
                # Use call_soon_threadsafe for thread safety
                self.hass.loop.call_soon_threadsafe(self._notify_status_callbacks, status)

    def _notify_status_callbacks(self, status: str) -> None:
        """Notify status callbacks on the main thread."""
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
            _LOGGER.debug("SIP Config - Server: %s, Port: %s, Username: %s", 
                         self.sip_server, self.sip_port, self.username)
            
            # Try to get local IP for NAT configuration
            import socket
            try:
                # Get local IP by connecting to a remote address
                s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
                s.connect(("8.8.8.8", 80))
                local_ip = s.getsockname()[0]
                s.close()
                _LOGGER.debug("Detected local IP: %s", local_ip)
            except Exception as e:
                local_ip = None
                _LOGGER.debug("Could not detect local IP: %s", e)
            
            # Create VoIPPhone instance with NAT configuration
            sip_phone = VoIPPhone(
                self.sip_server,
                self.sip_port,
                self.username,
                self.password,
                callCallback=call_callback,
                myIP=local_ip,  # Specify local IP for NAT
                sipPort=0,      # Use random port to avoid conflicts
                rtpPortLow=10000,
                rtpPortHigh=20000
            )
            
            _LOGGER.debug("Starting SIP phone")
            sip_phone.start()
            
            # Wait a moment for the phone to initialize and check status
            time.sleep(2)
            
            # Check phone status
            phone_status = sip_phone.get_status()
            _LOGGER.info("SIP phone status after start: %s", phone_status)
            
            # Only proceed if phone is registered
            from pyVoIP.VoIP import PhoneStatus
            if phone_status != PhoneStatus.REGISTERED:
                raise Exception(f"SIP phone failed to register. Status: {phone_status}")
            
            _LOGGER.debug("SIP phone registered successfully, making call to %s", self.gate_number)
            self._update_status(STATE_CALLING)
            
            # Try different number formats for Polish numbers
            number_to_call = self.gate_number
            if number_to_call.startswith('+48'):
                # Try without country code first
                alt_number = number_to_call[3:]  # Remove +48
                _LOGGER.info("Trying Polish number without country code: %s", alt_number)
                number_to_call = alt_number
            
            # Make the call
            call = sip_phone.call(number_to_call)
            _LOGGER.debug("Call initiated to %s, call object: %s", number_to_call, call)
            
            # Wait for the call to be established or get response
            start_time = time.time()
            max_wait_time = 30  # Increased to 30 seconds for troubleshooting
            last_logged_state = None
            
            while time.time() - start_time < max_wait_time:
                try:
                    state = call.state
                    elapsed = time.time() - start_time
                    
                    # Only log state changes to reduce spam
                    if state != last_logged_state:
                        _LOGGER.info("Call state changed to: %s (elapsed: %.1fs)", state, elapsed)
                        last_logged_state = state
                    
                    # Check for state changes
                    if state == CallState.ENDED:
                        _LOGGER.info("Call ended - this usually means the gate system processed the call")
                        call_completed = True
                        break
                    elif state == CallState.RINGING:
                        _LOGGER.info("Call is ringing at destination - success!")
                        self._update_status(STATE_RINGING)
                        # Wait a bit for the gate to process, then consider it successful
                        time.sleep(2)
                        call_completed = True
                        break
                    elif state == CallState.DIALING:
                        # Still trying to connect - wait longer
                        time.sleep(1)  # Check less frequently
                    else:
                        # For any other state, log it and wait
                        _LOGGER.info("Unexpected call state: %s", state)
                        time.sleep(0.5)
                        
                except InvalidStateError as e:
                    _LOGGER.debug("InvalidStateError during call state check: %s", e)
                    time.sleep(0.5)
                except Exception as e:
                    _LOGGER.error("Error checking call state: %s", e)
                    break
            
            # Analyze final state
            final_state = None
            try:
                final_state = call.state
                _LOGGER.info("Call completed with final state: %s", final_state)
            except Exception as e:
                _LOGGER.debug("Could not get final call state: %s", e)
            
            if not call_completed:
                if final_state == CallState.DIALING:
                    _LOGGER.error("Call remained in DIALING state for %d seconds - SIP routing issue", max_wait_time)
                    raise Exception(f"Call failed to progress beyond DIALING state after {max_wait_time} seconds. This suggests the SIP server cannot route calls to '{number_to_call}'. Please check: 1) Number format, 2) PSTN calling permissions, 3) Network/firewall settings")
                else:
                    _LOGGER.info("Call timeout reached with state: %s - assuming success", final_state)
                    call_completed = True
            
            # Clean up call object
            try:
                _LOGGER.debug("Cleaning up call, final state: %s", call.state)
            except:
                pass
            
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
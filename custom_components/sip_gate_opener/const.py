"""Constants for the SIP Gate Opener integration."""

DOMAIN = "sip_gate_opener"

# Configuration keys
CONF_SIP_SERVER = "sip_server"
CONF_SIP_PORT = "sip_port"
CONF_USERNAME = "username"
CONF_PASSWORD = "password"
CONF_GATE_NUMBER = "gate_number"
CONF_CALLER_ID = "caller_id"

# Default values
DEFAULT_SIP_PORT = 5060
DEFAULT_RING_DURATION = 1.0  # seconds

# Service names
SERVICE_OPEN_GATE = "open_gate"

# Sensor states
STATE_IDLE = "idle"
STATE_CONNECTING = "connecting"
STATE_CALLING = "calling"
STATE_RINGING = "ringing"
STATE_ANSWERED = "answered"
STATE_BUSY = "busy"
STATE_COMPLETED = "completed"
STATE_FAILED = "failed"
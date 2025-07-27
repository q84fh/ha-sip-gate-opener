# SIP Gate Opener for Home Assistant

A Home Assistant integration that allows you to open parking gates or doors by making SIP calls to a PSTN number. This integration is designed for systems where calling a specific number triggers the gate to open and then responds with a busy signal.

## Features

- **SIP Protocol Support**: Uses the pyVoIP library to make SIP calls
- **NAT Traversal**: Configured to work from behind NAT
- **One Ring and Hangup**: Automatically hangs up after one ring or when receiving a busy signal
- **HACS Compatible**: Easy installation through HACS
- **Config Flow**: Simple setup through Home Assistant UI
- **Button Entity**: Provides a button to trigger gate opening
- **Service**: Includes a service for automation use

## Installation

### Via HACS (Recommended)

1. Install HACS if you haven't already
2. Add this repository to HACS as a custom repository:
   - Go to HACS → Integrations → ⋮ → Custom repositories
   - Add `https://github.com/q84fh/sip-gate-opener` as an Integration
3. Install "SIP Gate Opener" from HACS
4. Restart Home Assistant

### Manual Installation

1. Copy the `sip_gate_opener` folder to your `custom_components` directory
2. Restart Home Assistant

## Configuration

1. Go to Settings → Devices & Services → Add Integration
2. Search for "SIP Gate Opener"
3. Fill in the required information:
   - **SIP Server**: Your SIP provider's server address
   - **SIP Port**: SIP server port (usually 5060)
   - **Username**: Your SIP account username
   - **Password**: Your SIP account password
   - **Gate Phone Number**: The PSTN number to call for gate opening
   - **Caller ID** (optional): Custom caller ID to display

## Usage

### Button Entity

After configuration, you'll have a button entity called "Open Gate" that you can:
- Add to your dashboard
- Use in automations
- Trigger manually

### Service

The integration provides a `sip_gate_opener.open_gate` service that can be used in automations:

```yaml
service: sip_gate_opener.open_gate
target:
  entity_id: button.open_gate
```

### Example Automation

```yaml
automation:
  - alias: "Open Gate with NFC Tag"
    trigger:
      platform: tag
      tag_id: "gate_opener_tag"
    action:
      service: sip_gate_opener.open_gate
      target:
        entity_id: button.open_gate
```

## How It Works

1. When triggered, the integration establishes a SIP connection to your provider
2. It dials the configured gate number
3. The gate system recognizes the authorized number and starts opening
4. The gate system responds with a busy signal or voice message
5. The integration automatically hangs up after one ring or upon receiving the busy signal
6. The SIP connection is properly closed

## NAT Configuration

This integration is configured to work behind NAT by:
- Auto-detecting the local IP address
- Using the SIP server hostname for proper routing
- Handling SIP responses appropriately for NAT environments

## Requirements

- Home Assistant 2023.1.0 or newer
- pyVoIP library (automatically installed)
- SIP account with PSTN calling capability
- Gate system that opens when called from authorized numbers

## Troubleshooting

### Common Issues

1. **Connection Fails**: Verify SIP server settings and credentials
2. **NAT Issues**: Ensure your router allows SIP traffic
3. **Gate Doesn't Open**: Confirm the phone number is authorized with the gate system
4. **Call Never Connects**: Check if the SIP provider supports PSTN calling

### Logs

Enable debug logging to troubleshoot issues:

```yaml
logger:
  logs:
    custom_components.sip_gate_opener: debug
    pyVoIP: debug
```

## Support

- [Issues](https://github.com/q84fh/sip-gate-opener/issues)
- [Discussions](https://github.com/q84fh/sip-gate-opener/discussions)

## License

This project is licensed under the MIT License - see the LICENSE file for details.
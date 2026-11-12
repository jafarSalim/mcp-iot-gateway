// ESP32 climate node for mcp-iot-gateway.
// Hardware: ESP32 DevKit, DHT22 on GPIO 4, relay (fan) on GPIO 26, LED on GPIO 2 (PWM).
// Libraries (Arduino Library Manager): PubSubClient by Nick O'Leary, ArduinoJson 7, DHT sensor library by Adafruit.
// Protocol (same as the simulator in internal/sim):
//   devices/<id>/state      retained JSON: sensors and actuators this node offers
//   devices/<id>/telemetry  every 2 s: {"temperature":..,"humidity":..,"fan":..}
//   devices/<id>/cmd        {"id":"<corr>","actuator":"fan"|"led","value":..}
//   devices/<id>/ack        {"id":"<corr>","actuator":..,"value":..,"ok":true|false}
#include <WiFi.h>
#include <PubSubClient.h>
#include <ArduinoJson.h>
#include <DHT.h>

const char* WIFI_SSID = "YOUR_WIFI";
const char* WIFI_PASS = "YOUR_PASSWORD";
const char* MQTT_HOST = "192.168.1.10";   // the computer running `gateway -listen 0.0.0.0:1883`
const uint16_t MQTT_PORT = 1883;
const char* NODE_ID = "esp32-lab";
const char* LOCATION = "lab";

const int PIN_DHT = 4, PIN_RELAY = 26, PIN_LED = 2;
DHT dht(PIN_DHT, DHT22);
WiFiClient net;
PubSubClient mqtt(net);
bool fanOn = false;
int ledLevel = 0;
unsigned long lastTelemetry = 0;

String topic(const char* leaf) { return String("devices/") + NODE_ID + "/" + leaf; }

void publishState() {
  JsonDocument doc;
  doc["id"] = NODE_ID;
  doc["name"] = "ESP32 climate node";
  doc["model"] = "ESP32-WROOM-32";
  doc["location"] = LOCATION;
  doc["sensors"]["temperature"]["unit"] = "°C";
  doc["sensors"]["humidity"]["unit"] = "%RH";
  doc["actuators"]["fan"]["type"] = "bool";
  doc["actuators"]["led"]["type"] = "int";
  doc["actuators"]["led"]["min"] = 0;
  doc["actuators"]["led"]["max"] = 255;
  char buf[512];
  size_t n = serializeJson(doc, buf);
  mqtt.publish(topic("state").c_str(), (const uint8_t*)buf, n, true);  // retained
}

void onMessage(char* t, byte* payload, unsigned int len) {
  JsonDocument cmd, ack;
  if (deserializeJson(cmd, payload, len)) return;
  const char* act = cmd["actuator"] | "";
  ack["id"] = cmd["id"];
  ack["actuator"] = act;
  ack["ok"] = false;
  if (strcmp(act, "fan") == 0 && cmd["value"].is<bool>()) {
    fanOn = cmd["value"].as<bool>();
    digitalWrite(PIN_RELAY, fanOn ? HIGH : LOW);
    ack["value"] = fanOn;
    ack["ok"] = true;
  } else if (strcmp(act, "led") == 0 && cmd["value"].is<int>()) {
    int v = cmd["value"].as<int>();
    if (v >= 0 && v <= 255) {
      ledLevel = v;
      analogWrite(PIN_LED, ledLevel);
      ack["value"] = ledLevel;
      ack["ok"] = true;
    }
  }
  if (!ack["ok"]) ack["error"] = "unsupported actuator or value";
  char buf[256];
  size_t n = serializeJson(ack, buf);
  mqtt.publish(topic("ack").c_str(), (const uint8_t*)buf, n, false);
}

void connectMqtt() {
  while (!mqtt.connected()) {
    if (mqtt.connect(NODE_ID)) {
      mqtt.subscribe(topic("cmd").c_str());
      publishState();
    } else {
      delay(1000);
    }
  }
}

void setup() {
  Serial.begin(115200);
  pinMode(PIN_RELAY, OUTPUT);
  pinMode(PIN_LED, OUTPUT);
  dht.begin();
  WiFi.begin(WIFI_SSID, WIFI_PASS);
  while (WiFi.status() != WL_CONNECTED) delay(300);
  Serial.printf("WiFi ok, IP %s\n", WiFi.localIP().toString().c_str());
  mqtt.setServer(MQTT_HOST, MQTT_PORT);
  mqtt.setBufferSize(768);
  mqtt.setCallback(onMessage);
}

void loop() {
  if (!mqtt.connected()) connectMqtt();
  mqtt.loop();
  if (millis() - lastTelemetry >= 2000) {
    lastTelemetry = millis();
    float t = dht.readTemperature(), h = dht.readHumidity();
    if (isnan(t) || isnan(h)) return;  // the DHT22 needs 2 s between reads; skip a failed one
    JsonDocument doc;
    doc["temperature"] = round(t * 100) / 100.0;
    doc["humidity"] = round(h * 10) / 10.0;
    doc["fan"] = fanOn;
    char buf[128];
    size_t n = serializeJson(doc, buf);
    mqtt.publish(topic("telemetry").c_str(), (const uint8_t*)buf, n, false);
  }
}

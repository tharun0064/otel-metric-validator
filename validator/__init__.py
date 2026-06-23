"""OTel <-> Oracle metric validation framework.

Cross-checks the metric values an OpenTelemetry collector ingests (from the
nroracledbreceiver fork) against ground truth obtained by running the receiver's
own monitoring SQL directly against Oracle.
"""

__all__ = [
    "config",
    "metric_map",
    "db_probe",
    "ingest_reader",
    "comparator",
    "report",
]

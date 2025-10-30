# Assisted by watsonx Code Assistant

import unittest
from unittest.mock import MagicMock, patch

from gputranslator import GpuTranslator


class TestGpuTranslator(unittest.TestCase):

    def test_populate_mapping(self):
        with patch("gputranslator.pynvml") as mock_pynvml:
            # Same setup as above
            mock_pynvml.nvmlDeviceGetCount.return_value = 1
            mock_handle = MagicMock()
            mock_pynvml.nvmlDeviceGetHandleByIndex.return_value = mock_handle
            mock_pynvml.nvmlDeviceGetUUID.return_value = b"single-gpu-uuid"

            # Test the class
            gpu_translator = GpuTranslator()

            # Assertions
            self.assertEqual(gpu_translator.device_count, 1)
            self.assertEqual(gpu_translator.mapping, {"single-gpu-uuid": 0})

    def test_uuid_to_index(self):
        with patch("gputranslator.pynvml") as mock_pynvml:
            # Same setup as above
            mock_pynvml.nvmlDeviceGetCount.return_value = 1
            mock_handle = MagicMock()
            mock_pynvml.nvmlDeviceGetHandleByIndex.return_value = mock_handle
            mock_pynvml.nvmlDeviceGetUUID.return_value = b"single-gpu-uuid"

            # Test the class
            gpu_translator = GpuTranslator()

            known_uuid = "single-gpu-uuid"
            index = gpu_translator.uuid_to_index(known_uuid)
            self.assertEqual(index, 0)

            unknown_uuid = "nonexistent_uuid"
            with self.assertRaises(ValueError):
                gpu_translator.uuid_to_index(unknown_uuid)

    def test_index_to_uuid(self):
        with patch("gputranslator.pynvml") as mock_pynvml:
            # Same setup as above
            mock_pynvml.nvmlDeviceGetCount.return_value = 1
            mock_handle = MagicMock()
            mock_pynvml.nvmlDeviceGetHandleByIndex.return_value = mock_handle
            mock_pynvml.nvmlDeviceGetUUID.return_value = b"single-gpu-uuid"

            # Test the class
            gpu_translator = GpuTranslator()

            known_index = 0
            uuid = gpu_translator.index_to_uuid(known_index)
            self.assertEqual(uuid, "single-gpu-uuid")

            unknown_index = gpu_translator.device_count  # One past the last known index
            with self.assertRaises(ValueError):
                gpu_translator.index_to_uuid(unknown_index)


if __name__ == "__main__":
    unittest.main()

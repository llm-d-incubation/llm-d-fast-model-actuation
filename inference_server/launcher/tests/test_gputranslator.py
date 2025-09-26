# Assisted by watsonx Code Assistant

import unittest

from gputranslator import GpuTranslator


class TestGpuTranslator(unittest.TestCase):

    def setUp(self):
        self.gpu_translator = GpuTranslator()
        # Predefined mapping for testing
        self.gpu_translator.mapping = {
            "uuid1": 0,
            "uuid2": 1,
        }
        self.gpu_translator.device_count = 2  # Example value
        self.gpu_translator.reverse_mapping = {
            v: k for k, v in self.gpu_translator.mapping.items()
        }

    def test_uuid_to_index(self):
        print(self.gpu_translator.mapping)
        known_uuid = "uuid1"
        index = self.gpu_translator.uuid_to_index(known_uuid)
        self.assertEqual(index, 0)

        known_uuid = "uuid2"
        index = self.gpu_translator.uuid_to_index(known_uuid)
        self.assertEqual(index, 1)

        unknown_uuid = "nonexistent_uuid"
        with self.assertRaises(ValueError):
            self.gpu_translator.uuid_to_index(unknown_uuid)

    def test_index_to_uuid(self):
        print(self.gpu_translator.mapping)
        known_index = 0
        uuid = self.gpu_translator.index_to_uuid(known_index)
        self.assertEqual(uuid, "uuid1")

        unknown_index = (
            self.gpu_translator.device_count
        )  # One past the last known index
        with self.assertRaises(ValueError):
            self.gpu_translator.index_to_uuid(unknown_index)


if __name__ == "__main__":
    unittest.main()
